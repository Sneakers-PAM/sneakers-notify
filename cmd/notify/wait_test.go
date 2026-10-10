// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	log "github.com/Bugs5382/go-log"
	bredis "github.com/Bugs5382/go-redis"
	"github.com/Sneakers-PAM/sneakers-notify/internal/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return strconv.Itoa(p)
}

// lateProxy starts forwarding 127.0.0.1:port to target after delay; until
// then nothing listens there, so connections are refused, as when the
// database comes up after the service.
func lateProxy(t *testing.T, port, target string, delay time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	wg.Go(func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		l, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Errorf("proxy listen: %v", err)
			return
		}
		go func() { <-ctx.Done(); _ = l.Close() }()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer func() { _ = c.Close() }()
				up, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer func() { _ = up.Close() }()
				go func() {
					_, _ = io.Copy(up, c)
					_ = up.Close()
				}()
				_, _ = io.Copy(c, up)
			})
		}
	})
}

// With Valkey unreachable at boot, notify waits: no error, the port answers
// liveness SERVING and readiness NOT_SERVING, and once Valkey answers the
// client connects. Needs NOTIFY_VALKEY_ADDR.
func TestConnectValkey_WaitsForALateServer(t *testing.T) {
	target := os.Getenv("NOTIFY_VALKEY_ADDR")
	if target == "" {
		t.Skip("set NOTIFY_VALKEY_ADDR to run the late-Valkey boot test")
	}
	proxyPort := freePort(t)
	const delay = 3 * time.Second
	lateProxy(t, proxyPort, target, delay)

	port := freePort(t)
	boot, err := server.StartBootHealth(port, log.Nop(), "valkey")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(boot.Stop)

	type connected struct {
		rc  *bredis.Client
		err error
	}
	done := make(chan connected, 1)
	start := time.Now()
	go func() {
		rc, err := connectValkey(context.Background(), boot, []bredis.Option{bredis.WithAddr("127.0.0.1:" + proxyPort)},
			bredis.WithWaitBackoff(100*time.Millisecond, 500*time.Millisecond))
		done <- connected{rc, err}
	}()

	conn, err := grpc.NewClient("127.0.0.1:"+port, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	hc := healthpb.NewHealthClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for svc, want := range map[string]healthpb.HealthCheckResponse_ServingStatus{
		server.LivenessService: healthpb.HealthCheckResponse_SERVING,
		"":                     healthpb.HealthCheckResponse_NOT_SERVING,
	} {
		resp, err := hc.Check(ctx, &healthpb.HealthCheckRequest{Service: svc}, grpc.WaitForReady(true))
		if err != nil || resp.GetStatus() != want {
			t.Fatalf("health %q while waiting = %v, %v; want %v", svc, resp.GetStatus(), err, want)
		}
	}

	select {
	case c := <-done:
		if c.err != nil {
			t.Fatalf("connectValkey = %v, want it to wait", c.err)
		}
		defer func() { _ = c.rc.Close() }()
		if waited := time.Since(start); waited < delay {
			t.Fatalf("connected after %v, before Valkey was reachable", waited)
		}
		if !c.rc.Healthy(context.Background()) {
			t.Fatal("client not healthy after connecting")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("connectValkey did not finish after Valkey came up")
	}
}
