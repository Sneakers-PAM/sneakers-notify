// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	log "github.com/Bugs5382/go-log"
	bredis "github.com/Bugs5382/go-redis"
	"github.com/Sneakers-PAM/sneakers-notify/internal/server"
	"github.com/rs/zerolog"
)

// mustBootHealth starts the boot health service on port; failing to listen
// is fatal.
func mustBootHealth(logger zerolog.Logger, port string, lg log.Logger) *server.BootHealth {
	boot, err := server.StartBootHealth(port, lg, "valkey")
	if err != nil {
		logger.Fatal().Err(err).Msg("boot health")
	}
	return boot
}

// connectValkey connects to Valkey, waiting with backoff while it (or its
// DNS name) isn't reachable yet, so a boot that comes up first never exits
// for it. Each failed attempt is logged and shown in the boot health report.
// A non-transient error (WRONGPASS, NOAUTH) and ctx ending are returned.
func connectValkey(ctx context.Context, boot *server.BootHealth, opts []bredis.Option, wait ...bredis.WaitOption) (*bredis.Client, error) {
	wait = append([]bredis.WaitOption{bredis.WithRetryHook(boot.RetryHook("valkey"))}, wait...)
	var rc *bredis.Client
	if err := bredis.WaitFor(ctx, func(ctx context.Context) error {
		var err error
		rc, err = bredis.Connect(ctx, opts...)
		return err
	}, wait...); err != nil {
		return nil, err
	}
	boot.Up("valkey")
	return rc, nil
}
