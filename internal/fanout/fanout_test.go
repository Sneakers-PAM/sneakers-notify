// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package fanout

import (
	"context"
	"testing"

	identityv1 "github.com/Sneakers-PAM/sneakers-identity/gen/go/sneakers/identity/v1"
	notifyv1 "github.com/Sneakers-PAM/sneakers-notify/gen/go/sneakers/notify/v1"
	"google.golang.org/grpc"
)

type fakeIdentity struct {
	identityv1.IdentityServiceClient
	groups    map[string]string   // name -> id
	members   map[string][]string // groupID -> userIDs
	adMembers map[string][]string // adName -> userIDs
	labels    map[string]string   // id -> name
}

func (f *fakeIdentity) ListGroups(ctx context.Context, in *identityv1.ListGroupsRequest, _ ...grpc.CallOption) (*identityv1.ListGroupsResponse, error) {
	var gs []*identityv1.Group
	for n, id := range f.groups {
		gs = append(gs, &identityv1.Group{Id: id, Name: n})
	}
	return &identityv1.ListGroupsResponse{Groups: gs}, nil
}
func (f *fakeIdentity) ListGroupMembers(ctx context.Context, in *identityv1.ListGroupMembersRequest, _ ...grpc.CallOption) (*identityv1.ListGroupMembersResponse, error) {
	var us []*identityv1.User
	for _, id := range f.members[in.GetGroupId()] {
		us = append(us, &identityv1.User{Id: id})
	}
	return &identityv1.ListGroupMembersResponse{Users: us}, nil
}
func (f *fakeIdentity) ListUsersByAdGroups(ctx context.Context, in *identityv1.ListUsersByAdGroupsRequest, _ ...grpc.CallOption) (*identityv1.ListUsersByAdGroupsResponse, error) {
	var us []*identityv1.User
	for _, n := range in.GetNames() {
		for _, id := range f.adMembers[n] {
			us = append(us, &identityv1.User{Id: id})
		}
	}
	return &identityv1.ListUsersByAdGroupsResponse{Users: us}, nil
}
func (f *fakeIdentity) ResolveUserLabels(ctx context.Context, in *identityv1.ResolveUserLabelsRequest, _ ...grpc.CallOption) (*identityv1.ResolveUserLabelsResponse, error) {
	var ls []*identityv1.UserLabel
	for _, id := range in.GetIds() {
		ls = append(ls, &identityv1.UserLabel{Id: id, Name: f.labels[id]})
	}
	return &identityv1.ResolveUserLabelsResponse{Labels: ls}, nil
}

func TestRecipientsExpandsAndDropsActor(t *testing.T) {
	r := Resolver{Identity: &fakeIdentity{
		groups:    map[string]string{"example-group": "g1"},
		members:   map[string][]string{"g1": {"user-a", "user-alice"}},
		adMembers: map[string][]string{"example-group": {"user-b"}},
	}}
	subs := []*notifyv1.InformedSubject{
		{Kind: "user", Name: "user-c"},
		{Kind: "group", Name: "example-group"},
		{Kind: "everyone"}, // ignored
	}
	got, err := r.Recipients(context.Background(), subs, "user-alice")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, id := range got {
		seen[id] = true
	}
	if seen["user-alice"] {
		t.Fatal("actor should be dropped")
	}
	if !seen["user-a"] || !seen["user-b"] || !seen["user-c"] {
		t.Fatalf("missing recipients: %v", got)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 deduped recipients, got %d: %v", len(got), got)
	}
}
