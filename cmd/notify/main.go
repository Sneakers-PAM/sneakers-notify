// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	log "github.com/Bugs5382/go-log"
	otel "github.com/Bugs5382/go-otel"
	bredis "github.com/Bugs5382/go-redis"
	identityv1 "github.com/Sneakers-PAM/sneakers-notify/gen/go/thirdparty/identity/v1"
	"github.com/Sneakers-PAM/sneakers-notify/internal/fanout"
	"github.com/Sneakers-PAM/sneakers-notify/internal/grpcsvc"
	"github.com/Sneakers-PAM/sneakers-notify/internal/health"
	"github.com/Sneakers-PAM/sneakers-notify/internal/server"
	"github.com/Sneakers-PAM/sneakers-notify/internal/store"
	"github.com/Sneakers-PAM/sneakers-notify/internal/workloadauth"
	goredis "github.com/redis/go-redis/v9"
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

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := log.New(serviceName)

	// notify has no database, so it reads its few settings straight from the
	// environment rather than through a config loader that requires one.
	grpcPort := env("GRPC_PORT", "9090")
	otlpEndpoint := env("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")

	// Service-to-service authentication fails closed: check it before anything
	// else so a missing issuer stops the boot.
	if _, _, err := workloadauth.ServerConfigFromEnv(os.Getenv); err != nil {
		logger.Fatal().Err(err).Msg("workload auth config")
	}

	otelShutdown, err := otel.Init(ctx, serviceName, otlpEndpoint)
	if err != nil {
		logger.Fatal().Err(err).Msg("otel init")
	}
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			logger.Warn().Err(err).Msg("otel shutdown")
		}
	}()

	// Redis (ephemeral inbox). REDIS_URL matches the gateway's default so
	// both services share one Redis in dev.
	redisURL := env("REDIS_URL", "redis://localhost:26379/0")
	opt, err := goredis.ParseURL(redisURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("redis url")
	}
	ropts := []bredis.Option{bredis.WithAddr(opt.Addr), bredis.WithDB(opt.DB)}
	if opt.Password != "" {
		ropts = append(ropts, bredis.WithPassword(opt.Password))
	}
	rc, err := bredis.Connect(ctx, ropts...)
	if err != nil {
		logger.Fatal().Err(err).Msg("redis connect")
	}

	// Identity (recipient fan-out).
	idAddr := env("IDENTITY_ADDR", "localhost:9192")
	// Identity authenticates notify by its projected ServiceAccount token,
	// re-read from WORKLOAD_TOKEN_FILE on every call.
	idAuth, err := server.ClientAuth(os.Getenv)
	if err != nil {
		logger.Fatal().Err(err).Msg("workload token")
	}
	if len(idAuth) > 0 {
		logger.Info().Str("env", workloadauth.EnvTokenFile).Msg("identity: calls carry the workload token")
	} else {
		logger.Warn().Msg("identity: " + workloadauth.EnvTokenFile + " unset; calls carry no workload token (local development only)")
	}
	idConn, err := grpc.NewClient(idAddr, append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), server.ClientStatsHandler()}, idAuth...)...)
	if err != nil {
		logger.Fatal().Err(err).Str("identity", idAddr).Msg("dial identity")
	}
	defer func() { _ = idConn.Close() }()

	st := store.NewRedis(rc, 90*24*time.Hour)
	fo := fanout.Resolver{Identity: identityv1.NewIdentityServiceClient(idConn)}
	svc := grpcsvc.New(st, fo)

	svcLog := log.NewLogger(serviceName)
	// Every caller is authenticated by its workload identity and checked
	// against grpcsvc.CallerPolicy: the vault sends events, the gateway reads
	// inboxes.
	authOpts, err := server.WorkloadAuth(ctx, os.Getenv, grpcsvc.CallerPolicy(), svcLog)
	if err != nil {
		logger.Fatal().Err(err).Msg("workload auth")
	}
	logger.Info().Str("port", grpcPort).Msg("starting")
	// Readiness follows Valkey: every RPC reads or writes the inbox there.
	// Identity only resolves the recipients and actor label of a new event, so
	// while it's down notify still serves the inbox and is only degraded.
	checker := health.New(svcLog, health.Valkey(rc.Redis()), health.GRPCPeer("identity", false, idConn))
	if err := server.RunWithHealth(ctx, grpcPort, svcLog, checker, func(gs *grpc.Server) {
		grpcsvc.RegisterServer(gs, svc)
	}, authOpts...); err != nil {
		logger.Fatal().Err(err).Msg("server exited")
	}
}
