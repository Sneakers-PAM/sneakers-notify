// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"testing"
	"time"

	bredis "github.com/Bugs5382/go-redis"
	"github.com/alicebob/miniredis/v2"
)

func newTestStore(t *testing.T) Store {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	c, err := bredis.Connect(context.Background(), bredis.WithAddr(mr.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	return NewRedis(c, time.Hour)
}

func TestPushListUnreadMarkRead(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.Push(ctx, "u1", Item{ID: "n1", Action: "secret.reveal"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Push(ctx, "u1", Item{ID: "n2", Action: "secret.delete"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(ctx, "u1", 50)
	if err != nil || len(got) != 2 || got[0].ID != "n2" { // newest first
		t.Fatalf("list: %+v err=%v", got, err)
	}
	if n, _ := s.Unread(ctx, "u1"); n != 2 {
		t.Fatalf("unread want 2 got %d", n)
	}
	ok, err := s.MarkRead(ctx, "u1", "n2")
	if err != nil || !ok {
		t.Fatalf("markread ok=%v err=%v", ok, err)
	}
	if n, _ := s.Unread(ctx, "u1"); n != 1 {
		t.Fatalf("unread after markread want 1 got %d", n)
	}
	if err := s.MarkAllRead(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Unread(ctx, "u1"); n != 0 {
		t.Fatalf("unread after markall want 0 got %d", n)
	}
}

func TestListCapAt100(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for i := 0; i < 120; i++ {
		if err := s.Push(ctx, "u1", Item{ID: string(rune('a' + i%26))}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.List(ctx, "u1", 1000)
	if len(got) > 100 {
		t.Fatalf("cap: got %d", len(got))
	}
}
