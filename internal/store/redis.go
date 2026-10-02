// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

// Package store persists notification inbox items in Redis. Ephemeral by
// design: capped per-user lists with a TTL. The audit chain is the durable
// record; loss here is acceptable.
package store

import (
	"context"
	"encoding/json"
	"time"

	bredis "github.com/Bugs5382/go-redis"
	goredis "github.com/redis/go-redis/v9"
)

const (
	listPrefix = "sneakers:notif:"
	maxItems   = 100
)

// Item is one stored inbox notification.
type Item struct {
	ID            string `json:"id"`
	Action        string `json:"action"`
	ResourceKind  string `json:"resourceKind"`
	ResourceID    string `json:"resourceId"`
	ResourceLabel string `json:"resourceLabel"`
	ActorLabel    string `json:"actorLabel"`
	OccurredAt    string `json:"occurredAt"`
	Read          bool   `json:"read"`
}

// Store is the notification persistence port.
type Store interface {
	Push(ctx context.Context, userID string, it Item) error
	List(ctx context.Context, userID string, limit int) ([]Item, error)
	Unread(ctx context.Context, userID string) (int, error)
	MarkRead(ctx context.Context, userID, id string) (bool, error)
	MarkAllRead(ctx context.Context, userID string) error
}

type redisStore struct {
	c   goredis.UniversalClient
	ttl time.Duration
}

// NewRedis builds a Redis-backed Store with the given item TTL.
func NewRedis(c *bredis.Client, ttl time.Duration) Store {
	return &redisStore{c: c.Redis(), ttl: ttl}
}

func (s *redisStore) key(u string) string { return listPrefix + u }

func (s *redisStore) Push(ctx context.Context, userID string, it Item) error {
	b, err := json.Marshal(it)
	if err != nil {
		return err
	}
	pipe := s.c.TxPipeline()
	pipe.LPush(ctx, s.key(userID), b)
	pipe.LTrim(ctx, s.key(userID), 0, maxItems-1)
	pipe.Expire(ctx, s.key(userID), s.ttl)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *redisStore) List(ctx context.Context, userID string, limit int) ([]Item, error) {
	if limit <= 0 || limit > maxItems {
		limit = maxItems
	}
	raws, err := s.c.LRange(ctx, s.key(userID), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(raws))
	for _, r := range raws {
		var it Item
		if json.Unmarshal([]byte(r), &it) == nil {
			out = append(out, it)
		}
	}
	return out, nil
}

// Unread counts unread items in the (capped) list. Deriving it from the list —
// rather than a separate counter — keeps the badge and the inbox in lockstep:
// an empty list is always zero unread, and the two can never drift.
func (s *redisStore) Unread(ctx context.Context, userID string) (int, error) {
	raws, err := s.c.LRange(ctx, s.key(userID), 0, maxItems-1).Result()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range raws {
		var it Item
		if json.Unmarshal([]byte(r), &it) == nil && !it.Read {
			n++
		}
	}
	return n, nil
}

func (s *redisStore) MarkRead(ctx context.Context, userID, id string) (bool, error) {
	raws, err := s.c.LRange(ctx, s.key(userID), 0, -1).Result()
	if err != nil {
		return false, err
	}
	for i, r := range raws {
		var it Item
		if json.Unmarshal([]byte(r), &it) != nil || it.ID != id {
			continue
		}
		if it.Read {
			return true, nil
		}
		it.Read = true
		b, _ := json.Marshal(it)
		if err := s.c.LSet(ctx, s.key(userID), int64(i), b).Err(); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (s *redisStore) MarkAllRead(ctx context.Context, userID string) error {
	raws, err := s.c.LRange(ctx, s.key(userID), 0, -1).Result()
	if err != nil {
		return err
	}
	pipe := s.c.TxPipeline()
	for i, r := range raws {
		var it Item
		if json.Unmarshal([]byte(r), &it) != nil || it.Read {
			continue
		}
		it.Read = true
		b, _ := json.Marshal(it)
		pipe.LSet(ctx, s.key(userID), int64(i), b)
	}
	_, err = pipe.Exec(ctx)
	return err
}
