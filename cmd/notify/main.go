// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Bugs5382/go-buildinfo/health"
	log "github.com/Bugs5382/go-log"
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

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tel := telemetry.FromEnv(os.Getenv)
	logger := log.NewLoggerWithOptions(serviceName, log.WithOutput(tel.Output()))
	telemetry.SetErrorHandler(logger)

	// notify has no database, so it reads its few settings straight from the
	// environment rather than through a config loader that requires one.
	grpcPort := env("GRPC_PORT", "9090")

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
	boot := mustBootHealth(logger, grpcPort)
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
	idConn, err := grpc.NewClient(idAddr, append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), server.ClientStatsHandler()}, idAuth...)...)
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
	if err := server.RunWithHealth(ctx, grpcPort, logger, checker, func(gs *grpc.Server) {
		grpcsvc.RegisterServer(gs, svc)
	}, authOpts...); err != nil {
		logger.Fatal(err, "server exited")
	}
}
