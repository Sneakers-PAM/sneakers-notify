// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"testing"

	notifyv1 "github.com/Sneakers-PAM/sneakers-notify/gen/go/sneakers/notify/v1"
	"github.com/Sneakers-PAM/sneakers-notify/internal/fanout"
	"github.com/Sneakers-PAM/sneakers-notify/internal/store"
)

// memStore is a trivial Store for handler tests.
type memStore struct{ items map[string][]store.Item }

func (m *memStore) Push(_ context.Context, u string, it store.Item) error {
	m.items[u] = append([]store.Item{it}, m.items[u]...)
	return nil
}
func (m *memStore) List(_ context.Context, u string, _ int) ([]store.Item, error) {
	return m.items[u], nil
}
func (m *memStore) Unread(_ context.Context, u string) (int, error) {
	n := 0
	for _, it := range m.items[u] {
		if !it.Read {
			n++
		}
	}
	return n, nil
}
func (m *memStore) MarkRead(_ context.Context, u, id string) (bool, error) {
	for i := range m.items[u] {
		if m.items[u][i].ID == id {
			m.items[u][i].Read = true
			return true, nil
		}
	}
	return false, nil
}
func (m *memStore) MarkAllRead(_ context.Context, u string) error {
	for i := range m.items[u] {
		m.items[u][i].Read = true
	}
	return nil
}

func TestNotifyEventWritesInbox(t *testing.T) {
	ctx := context.Background()
	// fanout with no identity client: user subjects still resolve.
	s := New(&memStore{items: map[string][]store.Item{}}, fanout.Resolver{})
	resp, err := s.NotifyEvent(ctx, &notifyv1.NotifyEventRequest{
		Action: "secret.reveal", ResourceKind: "secret", ResourceId: "sec-1",
		ResourceLabel: "prod-db", ActorUserId: "user-alice",
		InformedSubjects: []*notifyv1.InformedSubject{{Kind: "user", Name: "user-a"}},
	})
	if err != nil || resp.GetDelivered() != 1 {
		t.Fatalf("delivered=%d err=%v", resp.GetDelivered(), err)
	}
	list, _ := s.ListNotifications(ctx, &notifyv1.ListNotificationsRequest{UserId: "user-a"})
	if len(list.GetNotifications()) != 1 || list.GetNotifications()[0].GetResourceLabel() != "prod-db" {
		t.Fatalf("inbox: %+v", list.GetNotifications())
	}
	cnt, _ := s.UnreadCount(ctx, &notifyv1.UnreadCountRequest{UserId: "user-a"})
	if cnt.GetCount() != 1 {
		t.Fatalf("unread=%d", cnt.GetCount())
	}
}
