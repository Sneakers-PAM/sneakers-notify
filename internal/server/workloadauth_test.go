// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	log "github.com/Bugs5382/go-log"
	"github.com/Sneakers-PAM/sneakers-notify/internal/workloadauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestWorkloadAuthUnsetIssuerFailsToBoot(t *testing.T) {
	_, err := WorkloadAuth(context.Background(), envOf(nil), workloadauth.Policy{}, log.Nop())
	if !errors.Is(err, workloadauth.ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestWorkloadAuthExplicitlyDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts, err := WorkloadAuth(ctx, envOf(map[string]string{workloadauth.EnvAuthMode: workloadauth.AuthDisabled}), workloadauth.Policy{}, log.Nop())
	if err != nil || len(opts) != 0 {
		t.Fatalf("opts=%d err=%v, want none", len(opts), err)
	}
}

func TestWorkloadAuthEnabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts, err := WorkloadAuth(ctx, envOf(map[string]string{
		workloadauth.EnvIssuer:                 "https://issuer.example.org",
		workloadauth.EnvAllowedServiceAccounts: "sneakers/sneakers-gateway",
	}), workloadauth.Policy{}, log.Nop())
	if err != nil || len(opts) != 2 {
		t.Fatalf("opts=%d err=%v, want the unary and stream interceptors", len(opts), err)
	}
}

func TestClientAuth(t *testing.T) {
	if opts, err := ClientAuth(envOf(nil)); err != nil || len(opts) != 0 {
		t.Fatalf("unset: opts=%d err=%v", len(opts), err)
	}
	if _, err := ClientAuth(envOf(map[string]string{workloadauth.EnvTokenFile: "/nonexistent/token"})); err == nil {
		t.Fatal("a missing token file must fail")
	}
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if opts, err := ClientAuth(envOf(map[string]string{workloadauth.EnvTokenFile: p})); err != nil || len(opts) != 1 {
		t.Fatalf("set: opts=%d err=%v", len(opts), err)
	}
}

// TestClientAuthSendsTheTokenOnEveryCall dials a real gRPC server with the
// ClientAuth options and checks each call carries the token as it is on disk
// at that moment, so a token the kubelet rotates in place is picked up.
func TestClientAuthSendsTheTokenOnEveryCall(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("token-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := ClientAuth(envOf(map[string]string{workloadauth.EnvTokenFile: p}))
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan string, 2)
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		seen <- strings.Join(md.Get("authorization"), ",")
		return h(ctx, req)
	}))
	healthpb.RegisterHealthServer(gs, health.NewServer())
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", append([]grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, auth...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	hc := healthpb.NewHealthClient(conn)
	for _, want := range []string{"token-one", "token-two"} {
		if err := os.WriteFile(p, []byte(want), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := hc.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
			t.Fatal(err)
		}
		if got := <-seen; got != "Bearer "+want {
			t.Fatalf("authorization %q, want %q", got, "Bearer "+want)
		}
	}
}
