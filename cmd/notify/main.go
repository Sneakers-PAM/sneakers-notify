// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Bugs5382/go-buildinfo/health"
	log "github.com/Bugs5382/go-log"
	"github.com/Bugs5382/go-mtls"
	otel "github.com/Bugs5382/go-otel"
	workloadauth "github.com/Bugs5382/go-workload-identity"
	identityv1 "github.com/Sneakers-PAM/sneakers-notify/gen/go/thirdparty/identity/v1"
	"github.com/Sneakers-PAM/sneakers-notify/internal/fanout"
	"github.com/Sneakers-PAM/sneakers-notify/internal/grpcsvc"
	"github.com/Sneakers-PAM/sneakers-notify/internal/server"
	"github.com/Sneakers-PAM/sneakers-notify/internal/store"
	"github.com/Sneakers-PAM/sneakers-notify/internal/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const serviceName = "notify"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// startOtel starts export and returns the flush to run on the way out.
func startOtel(ctx context.Context, logger log.Logger, tel telemetry.Settings) func() {
	otelShutdown, err := otel.InitWithOptions(ctx, serviceName, tel.Endpoint, tel.Options()...)
	if err != nil {
		logger.Fatal(err, "otel init")
	}
	if tel.Logs || tel.InsecureSkipVerify {
		logger.Info("otlp export options", log.F("otlp_logs", tel.Logs), log.F("otlp_insecure_skip_verify", tel.InsecureSkipVerify))
	}
	if tel.Logs && tel.InsecureSkipVerify {
		logger.Warn("otlp logs stay off while certificate checks are skipped")
	}
	return func() {
		if err := otelShutdown(context.Background()); err != nil {
			logger.Warn("otel shutdown", log.F("error", err.Error()))
		}
	}
}

// mustMTLS reads the mutual-TLS settings (MTLS_CERT_FILE, MTLS_KEY_FILE,
// MTLS_CA_FILE): plaintext (nil) when none is set, a failed boot when it is half
// set. With it on, grpcPort takes only verified clients and the kubelet probes
// the returned plaintext health-only port, because a probe cannot present a
// client certificate. Without it the probes use grpcPort, as before.
func mustMTLS(lg log.Logger, grpcPort string) (*mtls.Provider, string) {
	mt, err := server.MTLSFromEnv(lg)
	if err != nil {
		lg.Fatal(err, "mtls config")
	}
	if mt == nil {
		return nil, grpcPort
	}
	probePort := env("HEALTH_PORT", "9091")
	if probePort == grpcPort {
		lg.Fatal(errors.New("HEALTH_PORT equals GRPC_PORT"), "mtls config")
	}
	return mt, probePort
}

// identityCreds is how notify dials identity: mutual TLS when configured,
// plaintext for local development.
func identityCreds(mt *mtls.Provider) grpc.DialOption {
	if mt != nil {
		return mt.DialOption()
	}
	return grpc.WithTransportCredentials(insecure.NewCredentials())
}

// serveMTLS adds the mutual-TLS server option to opts and starts the
// plaintext health-only port; with mt nil it changes nothing.
func serveMTLS(ctx context.Context, mt *mtls.Provider, probePort string, lg log.Logger, checker *health.Checker, opts []grpc.ServerOption) []grpc.ServerOption {
	if mt == nil {
		return opts
	}
	go func() {
		if err := server.RunHealthOnly(ctx, probePort, lg, checker); err != nil {
			lg.Fatal(err, "health port exited")
		}
	}()
	return append(opts, mt.ServerOption())
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tel := telemetry.FromEnv(os.Getenv)
	logger := log.NewLoggerWithOptions(serviceName, log.WithOutput(tel.Output()))

	// notify has no database, so it reads its few settings straight from the
	// environment rather than through a config loader that requires one.
	grpcPort := env("GRPC_PORT", "9090")

	mt, probePort := mustMTLS(logger, grpcPort)

	// Service-to-service authentication fails closed: check it before anything
	// else so a missing issuer stops the boot.
	if _, _, err := server.WorkloadConfigFromEnv(os.Getenv); err != nil {
		logger.Fatal(err, "workload auth config")
	}

	defer startOtel(ctx, logger, tel)()

	// Redis (ephemeral inbox). REDIS_URL matches the gateway's default so
	// both services share one Redis in dev.
	ropts, err := redisOptions(env("REDIS_URL", "redis://localhost:26379/0"))
	if err != nil {
		logger.Fatal(err, "redis url")
	}
	// The port answers health from here on, while the boot waits for Valkey:
	// liveness SERVING, so the startup probe passes, and readiness
	// NOT_SERVING with valkey listed down until it is reached.
	boot := mustBootHealth(logger, probePort)
	rc, err := connectValkey(ctx, boot, ropts)
	if err != nil {
		if ctx.Err() != nil {
			boot.Stop()
			logger.Info("stopped while waiting for valkey")
			return
		}
		logger.Fatal(err, "redis connect")
	}

	// Identity (recipient fan-out).
	idAddr := env("IDENTITY_ADDR", "localhost:9192")
	// Identity authenticates notify by its projected ServiceAccount token,
	// re-read from WORKLOAD_TOKEN_FILE on every call.
	idAuth, err := server.ClientAuth(os.Getenv)
	if err != nil {
		logger.Fatal(err, "workload token")
	}
	if len(idAuth) > 0 {
		logger.Info("identity: calls carry the workload token", log.F("env", workloadauth.EnvTokenFile))
	} else {
		logger.Warn("identity: " + workloadauth.EnvTokenFile + " unset; calls carry no workload token (local development only)")
	}
	idConn, err := grpc.NewClient(idAddr, append([]grpc.DialOption{identityCreds(mt), server.ClientStatsHandler()}, idAuth...)...)
	if err != nil {
		logger.Fatal(err, "dial identity", log.F("identity", idAddr))
	}
	defer func() { _ = idConn.Close() }()

	st := store.NewRedis(rc, 90*24*time.Hour)
	fo := fanout.Resolver{Identity: identityv1.NewIdentityServiceClient(idConn)}
	svc := grpcsvc.New(st, fo)

	// Every caller is authenticated by its workload identity and checked
	// against grpcsvc.CallerPolicy: the vault sends events, the gateway reads
	// inboxes.
	workloadVerifier, authOpts, err := server.WorkloadAuth(ctx, os.Getenv, grpcsvc.CallerPolicy(), logger)
	if err != nil {
		logger.Fatal(err, "workload auth")
	}
	logger.Info("starting", log.F("port", grpcPort))
	// Readiness follows Valkey: every RPC reads or writes the inbox there.
	// Identity only resolves the recipients and actor label of a new event, so
	// while it's down notify still serves the inbox and is only degraded. The
	// workload-identity verifier is required too once authentication is on: no
	// caller can be checked before its key set loads. It's left out when
	// authentication is disabled (verifier nil).
	deps := []health.Dependency{server.Valkey(rc.Redis()), server.GRPCPeer("identity", false, idConn)}
	if workloadVerifier != nil {
		deps = append(deps, server.WorkloadIdentity(workloadVerifier))
	}
	if dep, ok := tel.Dependency(); ok {
		deps = append(deps, dep)
	}
	checker, err := server.NewChecker(logger, deps)
	if err != nil {
		logger.Fatal(err, "health checker")
	}
	boot.Stop()
	authOpts = serveMTLS(ctx, mt, probePort, logger, checker, authOpts)
	if err := server.RunWithHealth(ctx, grpcPort, logger, checker, func(gs *grpc.Server) {
		grpcsvc.RegisterServer(gs, svc)
	}, authOpts...); err != nil {
		logger.Fatal(err, "server exited")
	}
}
