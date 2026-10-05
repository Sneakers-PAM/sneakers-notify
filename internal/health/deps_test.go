// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	log "github.com/Bugs5382/go-log"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestValkey_DownWhileTheServerIsGone(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := goredis.NewClient(&goredis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rc.Close() })
	dep := Valkey(rc)
	if dep.Name != "valkey" || !dep.Required {
		t.Fatalf("dep = %+v, want required valkey", dep)
	}
	if err := dep.Check(context.Background()); err != nil {
		t.Fatalf("up: %v", err)
	}
	mr.Close()
	err := dep.Check(context.Background())
	if err == nil {
		t.Fatal("down: no error")
	}
	if c := Classify(err); c != ClassRefused && c != ClassUnavailable {
		t.Fatalf("class = %q, want refused or unavailable", c)
	}
	if err := mr.Restart(); err != nil {
		t.Fatal(err)
	}
	if err := dep.Check(context.Background()); err != nil {
		t.Fatalf("after restart: %v", err)
	}
}

func TestGRPCPeer_FollowsThePeersHealth(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := grpchealth.NewServer()
	s := grpc.NewServer()
	healthpb.RegisterHealthServer(s, hs)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	dep := GRPCPeer("identity", false, conn)
	if dep.Name != "identity" || dep.Required {
		t.Fatalf("dep = %+v, want optional identity", dep)
	}
	if err := dep.Check(context.Background()); err != nil {
		t.Fatalf("serving: %v", err)
	}
	hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	if err := dep.Check(context.Background()); Classify(err) != ClassUnavailable {
		t.Fatalf("not serving: %v (class %q), want unavailable", err, Classify(err))
	}
	s.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), CheckTimeout)
	defer cancel()
	if err := dep.Check(ctx); err == nil {
		t.Fatal("stopped peer: no error")
	}
}

// TestValkey_StopAndStartMidTest runs against a throwaway Valkey container it
// stops and starts again. Set NOTIFY_VALKEY_ADDR (host:port, a fixed host
// port, since a random one can change on restart) and NOTIFY_VALKEY_CONTAINER
// (the container's name) to run it.
func TestValkey_StopAndStartMidTest(t *testing.T) {
	addr, name := os.Getenv("NOTIFY_VALKEY_ADDR"), os.Getenv("NOTIFY_VALKEY_CONTAINER")
	if addr == "" || name == "" {
		t.Skip("set NOTIFY_VALKEY_ADDR and NOTIFY_VALKEY_CONTAINER to run the Valkey stop/start test")
	}
	ctx := context.Background()
	rc := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = rc.Close() })

	clk := time.Now()
	c := New(log.Nop(), Valkey(rc))
	c.now = func() time.Time { return clk }
	report := func() Report { clk = clk.Add(CacheTTL); return c.Report(ctx) }

	if r := report(); r.Status != StateOK {
		t.Fatalf("before the stop: %+v", r)
	}
	docker(t, "stop", name)
	stopped := true
	t.Cleanup(func() {
		if stopped {
			_ = exec.Command("docker", "start", name).Run()
		}
	})
	if r := report(); r.Status != StateDown || r.Dependencies[0].Error == "" {
		t.Fatalf("while stopped: %+v", r)
	}
	docker(t, "start", name)
	stopped = false
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := report()
		if r.Status == StateOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no recovery after the restart: %+v", r)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func docker(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %v: %v (%s)", args, err, out)
	}
}
