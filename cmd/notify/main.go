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
	identityv1 "github.com/Sneakers-PAM/sneakers-identity/gen/go/sneakers/identity/v1"
	"github.com/Sneakers-PAM/sneakers-notify/internal/fanout"
	"github.com/Sneakers-PAM/sneakers-notify/internal/grpcsvc"
	"github.com/Sneakers-PAM/sneakers-notify/internal/server"
	"github.com/Sneakers-PAM/sneakers-notify/internal/store"
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
	idConn, err := grpc.NewClient(idAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), server.ClientStatsHandler())
	if err != nil {
		logger.Fatal().Err(err).Str("identity", idAddr).Msg("dial identity")
	}
	defer func() { _ = idConn.Close() }()

	st := store.NewRedis(rc, 90*24*time.Hour)
	fo := fanout.Resolver{Identity: identityv1.NewIdentityServiceClient(idConn)}
	svc := grpcsvc.New(st, fo)

	logger.Info().Str("port", grpcPort).Msg("starting")
	if err := server.RunWithLogger(ctx, grpcPort, log.NewLogger(serviceName), func(gs *grpc.Server) {
		grpcsvc.RegisterServer(gs, svc)
	}); err != nil {
		logger.Fatal().Err(err).Msg("server exited")
	}
}
