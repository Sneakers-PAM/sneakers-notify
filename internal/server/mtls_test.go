// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	log "github.com/Bugs5382/go-log"
	"github.com/Bugs5382/go-mtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// testPKI writes a CA and one leaf for host under dir and returns the three
// paths. The names are invented; nothing here is a real identifier.
func testPKI(t *testing.T, dir, host string) (cert, key, ca string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host},
		DNSNames: []string{host}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, typ string, der []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return write("tls.crt", "CERTIFICATE", leafDER), write("tls.key", "EC PRIVATE KEY", keyDER), write("ca.crt", "CERTIFICATE", caDER)
}

func clearMTLSEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{mtls.EnvCertFile, mtls.EnvKeyFile, mtls.EnvCAFile} {
		t.Setenv(k, "")
	}
}

// With none of the three files named, notify stays plaintext (local
// development); nothing is built.
func TestMTLSFromEnv_UnsetIsPlaintext(t *testing.T) {
	clearMTLSEnv(t)
	p, err := MTLSFromEnv(log.Nop())
	if err != nil || p != nil {
		t.Fatalf("MTLSFromEnv() = %v, %v; want nil, nil", p, err)
	}
}

// A half-configured mTLS must stop the boot rather than fall back to
// plaintext without anyone noticing.
func TestMTLSFromEnv_PartialFailsClosed(t *testing.T) {
	clearMTLSEnv(t)
	t.Setenv(mtls.EnvCertFile, "/nonexistent/tls.crt")
	if p, err := MTLSFromEnv(log.Nop()); err == nil || p != nil {
		t.Fatalf("MTLSFromEnv() = %v, %v; want an error", p, err)
	}
}

func TestMTLSFromEnv_UnreadableFileFails(t *testing.T) {
	clearMTLSEnv(t)
	t.Setenv(mtls.EnvCertFile, "/nonexistent/tls.crt")
	t.Setenv(mtls.EnvKeyFile, "/nonexistent/tls.key")
	t.Setenv(mtls.EnvCAFile, "/nonexistent/ca.crt")
	if _, err := MTLSFromEnv(log.Nop()); err == nil {
		t.Fatal("MTLSFromEnv() with missing files returned no error")
	}
}

func mtlsProvider(t *testing.T) *mtls.Provider {
	t.Helper()
	clearMTLSEnv(t)
	cert, key, ca := testPKI(t, t.TempDir(), "localhost")
	t.Setenv(mtls.EnvCertFile, cert)
	t.Setenv(mtls.EnvKeyFile, key)
	t.Setenv(mtls.EnvCAFile, ca)
	p, err := MTLSFromEnv(log.Nop())
	if err != nil || p == nil {
		t.Fatalf("MTLSFromEnv() = %v, %v", p, err)
	}
	return p
}

// The mTLS port refuses a plaintext caller, while the separate health port
// answers a plaintext probe: the kubelet cannot present a client certificate.
func TestRunWithHealth_MTLSPortRefusesPlaintextAndHealthPortAnswers(t *testing.T) {
	p := mtlsProvider(t)
	mainPort, healthPort := freePort(t), freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- RunWithHealth(ctx, mainPort, log.Nop(), nil, func(*grpc.Server) {}, p.ServerOption()) }()
	go func() { done <- RunHealthOnly(ctx, healthPort, log.Nop(), nil) }()

	// Plain health port: SERVING for readiness and liveness.
	hc := bootClient(t, healthPort)
	if st, _ := bootCheck(t, hc, ""); st != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health port readiness = %v, want SERVING", st)
	}
	if st, _ := bootCheck(t, hc, LivenessService); st != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health port liveness = %v, want SERVING", st)
	}

	// Plaintext on the mTLS port is refused.
	pc := bootClient(t, mainPort)
	ctx2, cancel2 := context.WithTimeout(ctx, 3*time.Second)
	defer cancel2()
	if _, err := pc.Check(ctx2, &healthpb.HealthCheckRequest{}); err == nil {
		t.Fatal("plaintext call to the mTLS port succeeded")
	}

	// A client with a certificate from the same CA gets through.
	conn, err := grpc.NewClient("localhost:"+mainPort, p.DialOption())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx3, cancel3 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel3()
	if _, err := healthpb.NewHealthClient(conn).Check(ctx3, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("mTLS call failed: %v", err)
	}

	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("server returned %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("server did not stop")
		}
	}
}

// The health-only server carries the health service and nothing else, so
// the plaintext port exposes no notify RPC and no reflection.
func TestRunHealthOnly_ServesNothingElse(t *testing.T) {
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = RunHealthOnly(ctx, port, log.Nop(), nil) }()
	hc := bootClient(t, port)
	if st, _ := bootCheck(t, hc, ""); st != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("readiness = %v, want SERVING", st)
	}
	conn, err := grpc.NewClient("127.0.0.1:"+port, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	err = conn.Invoke(ctx, "/sneakers.notify.v1.NotifyService/ListInbox", &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("notify RPC on the health-only port = %v, want Unimplemented", err)
	}
}
