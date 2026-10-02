// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

// Package grpcsvc implements NotifyService: fan an Informed event into inboxes
// and serve the per-user inbox. Best-effort; the audit chain is the record.
package grpcsvc

import (
	"context"
	"time"

	notifyv1 "github.com/Sneakers-PAM/sneakers-notify/gen/go/sneakers/notify/v1"
	"github.com/Sneakers-PAM/sneakers-notify/internal/fanout"
	"github.com/Sneakers-PAM/sneakers-notify/internal/safeconv"
	"github.com/Sneakers-PAM/sneakers-notify/internal/store"
	"github.com/google/uuid"
	"google.golang.org/grpc"
)

// Server implements notifyv1.NotifyServiceServer.
type Server struct {
	notifyv1.UnimplementedNotifyServiceServer
	store store.Store
	fo    fanout.Resolver
}

// New builds a Server over the given store + fan-out resolver.
func New(st store.Store, fo fanout.Resolver) *Server { return &Server{store: st, fo: fo} }

// RegisterServer wires the Server into gRPC.
func RegisterServer(gs *grpc.Server, s *Server) { notifyv1.RegisterNotifyServiceServer(gs, s) }

func (s *Server) NotifyEvent(ctx context.Context, req *notifyv1.NotifyEventRequest) (*notifyv1.NotifyEventResponse, error) {
	recipients, err := s.fo.Recipients(ctx, req.GetInformedSubjects(), req.GetActorUserId())
	if err != nil {
		return nil, err
	}
	occurred := req.GetOccurredAt()
	if occurred == "" {
		occurred = time.Now().UTC().Format(time.RFC3339Nano)
	}
	actorLabel := s.fo.ActorLabel(ctx, req.GetActorUserId())
	var delivered int32
	for _, uid := range recipients {
		it := store.Item{
			ID:            uuid.NewString(),
			Action:        req.GetAction(),
			ResourceKind:  req.GetResourceKind(),
			ResourceID:    req.GetResourceId(),
			ResourceLabel: req.GetResourceLabel(),
			ActorLabel:    actorLabel,
			OccurredAt:    occurred,
		}
		if err := s.store.Push(ctx, uid, it); err == nil {
			delivered++
		}
	}
	return &notifyv1.NotifyEventResponse{Delivered: delivered}, nil
}

func (s *Server) ListNotifications(ctx context.Context, req *notifyv1.ListNotificationsRequest) (*notifyv1.ListNotificationsResponse, error) {
	items, err := s.store.List(ctx, req.GetUserId(), int(req.GetLimit()))
	if err != nil {
		return nil, err
	}
	out := make([]*notifyv1.Notification, 0, len(items))
	for _, it := range items {
		out = append(out, &notifyv1.Notification{
			Id: it.ID, Action: it.Action, ResourceKind: it.ResourceKind,
			ResourceId: it.ResourceID, ResourceLabel: it.ResourceLabel,
			ActorLabel: it.ActorLabel, OccurredAt: it.OccurredAt, Read: it.Read,
		})
	}
	return &notifyv1.ListNotificationsResponse{Notifications: out}, nil
}

func (s *Server) UnreadCount(ctx context.Context, req *notifyv1.UnreadCountRequest) (*notifyv1.UnreadCountResponse, error) {
	n, err := s.store.Unread(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	return &notifyv1.UnreadCountResponse{Count: safeconv.Int32(n)}, nil
}

func (s *Server) MarkRead(ctx context.Context, req *notifyv1.MarkReadRequest) (*notifyv1.MarkReadResponse, error) {
	ok, err := s.store.MarkRead(ctx, req.GetUserId(), req.GetId())
	if err != nil {
		return nil, err
	}
	return &notifyv1.MarkReadResponse{Ok: ok}, nil
}

func (s *Server) MarkAllRead(ctx context.Context, req *notifyv1.MarkAllReadRequest) (*notifyv1.MarkAllReadResponse, error) {
	if err := s.store.MarkAllRead(ctx, req.GetUserId()); err != nil {
		return nil, err
	}
	return &notifyv1.MarkAllReadResponse{Ok: true}, nil
}
