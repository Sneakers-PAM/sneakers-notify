// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	bredis "github.com/Bugs5382/go-redis"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// tlsServer starts a TLS-only Redis with a throwaway certificate and returns
// its address and the pool that trusts it.
func tlsServer(t *testing.T) (string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "cache.example.org"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		IsCA:        true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	m := miniredis.NewMiniRedis()
	cfg := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	if err := m.StartTLS(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m.Addr(), pool
}

func TestRedissURLConnectsOverTLS(t *testing.T) {
	addr, pool := tlsServer(t)
	opt, err := goredis.ParseURL("rediss://" + addr + "/0")
	if err != nil {
		t.Fatal(err)
	}
	opt.TLSConfig.RootCAs = pool
	ropts, err := redisOptionsFrom(opt)
	if err != nil {
		t.Fatal(err)
	}
	c, err := bredis.Connect(context.Background(), ropts...)
	if err != nil {
		t.Fatalf("connect over TLS: %v", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Redis().Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping over TLS: %v", err)
	}
}

// A rediss:// URL must never fall back to plain text: against a server whose
// certificate isn't trusted the connection fails on that certificate.
func TestRedissURLVerifiesTheCertificate(t *testing.T) {
	addr, _ := tlsServer(t)
	ropts, err := redisOptions("rediss://" + addr + "/0")
	if err != nil {
		t.Fatal(err)
	}
	c, err := bredis.Connect(context.Background(), ropts...)
	if err == nil {
		_ = c.Close()
		t.Fatal("connected to a server with an untrusted certificate")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("err = %v, want a certificate failure", err)
	}
}

func TestRedisURLOptionsItCantHonourAreRefused(t *testing.T) {
	const fake = "not-a-real-password"
	for name, raw := range map[string]string{
		"username":     "redis://alice:" + fake + "@cache.example.org:6379/0",
		"query option": "redis://:" + fake + "@cache.example.org:6379/0?pool_size=5",
		"skip_verify":  "rediss://cache.example.org:6379/0?skip_verify=true",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := redisOptions(raw)
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if strings.Contains(err.Error(), fake) {
				t.Fatalf("the error quotes the password: %v", err)
			}
		})
	}
}

func TestPlainRedisURLStillWorks(t *testing.T) {
	m := miniredis.RunT(t)
	ropts, err := redisOptions("redis://" + m.Addr() + "/0")
	if err != nil {
		t.Fatal(err)
	}
	c, err := bredis.Connect(context.Background(), ropts...)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	_ = c.Close()
}
