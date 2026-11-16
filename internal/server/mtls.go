// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Bugs5382/go-buildinfo/health"
	log "github.com/Bugs5382/go-log"
	"github.com/Bugs5382/go-mtls"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// MTLSFromEnv builds the mutual-TLS provider from MTLS_CERT_FILE,
// MTLS_KEY_FILE and MTLS_CA_FILE (the owner's go-mtls package). With none of
// the three set it returns nil: the service stays plaintext, for local
// development. With some set, or with files that cannot be read, it fails, so
// a half-configured chart never falls back to plaintext silently.
func MTLSFromEnv(lg log.Logger) (*mtls.Provider, error) {
	if os.Getenv(mtls.EnvCertFile) == "" && os.Getenv(mtls.EnvKeyFile) == "" && os.Getenv(mtls.EnvCAFile) == "" {
		lg.Warn("mtls: " + mtls.EnvCertFile + ", " + mtls.EnvKeyFile + " and " + mtls.EnvCAFile + " unset; serving and calling plaintext (local development only)")
		return nil, nil
	}
	files, err := mtls.FilesFromEnv()
	if err != nil {
		return nil, err
	}
	p, err := mtls.NewProvider(files, mtls.WithErrorHandler(func(err error) {
		// A renewal that cannot be read keeps the last good files in use.
		lg.Error(err, "mtls: certificate reload failed; keeping the last good files")
	}))
	if err != nil {
		return nil, fmt.Errorf("mtls: %w", err)
	}
	lg.Info("mtls: serving and calling over mutual TLS")
	return p, nil
}

// RunHealthOnly serves the standard health service (readiness follows
// checker, "liveness" is the process) on a plaintext :port and nothing else:
// no notify RPC, no reflection. It exists because the kubelet's gRPC probe
// cannot present a client certificate, so the mutual-TLS port cannot be probed.
// It reads the checker the main server runs; it does not start it. It returns
// nil once ctx is cancelled.
func RunHealthOnly(ctx context.Context, port string, lg log.Logger, checker *health.Checker) error {
	hs, bi, err := newHealth(checker)
	if err != nil {
		return fmt.Errorf("health: %w", err)
	}
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return fmt.Errorf("health listen: %w", err)
	}
	s := grpc.NewServer(grpc.ChainUnaryInterceptor(RecoveryUnaryInterceptor(lg), bi.UnaryServerInterceptor(), reportUnaryInterceptor(checker)))
	healthpb.RegisterHealthServer(s, hs)

	biCtx, stopBI := context.WithCancel(ctx)
	defer stopBI()
	go bi.Run(biCtx)

	lg.Info("health-only port serving (plaintext)", log.F("port", port))
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(lis) }()
	select {
	case <-ctx.Done():
		hs.Shutdown()
		stopped := make(chan struct{})
		go func() { s.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(gracefulStopTimeout):
			s.Stop()
		}
		if err := <-errCh; err != nil {
			lg.Warn("health port stopped with an error", log.F("error", err.Error()))
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	}
}
