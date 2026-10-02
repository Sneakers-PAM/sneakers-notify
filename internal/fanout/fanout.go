// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

// Package fanout expands Informed subjects into recipient user IDs using the
// identity directory (group membership + AD-group membership), and resolves
// actor display labels.
package fanout

import (
	"context"

	identityv1 "github.com/Sneakers-PAM/sneakers-identity/gen/go/sneakers/identity/v1"
	notifyv1 "github.com/Sneakers-PAM/sneakers-notify/gen/go/sneakers/notify/v1"
)

// Resolver turns subjects into recipients.
type Resolver struct {
	Identity identityv1.IdentityServiceClient
}

// Recipients expands subjects to a deduped user-ID set, minus excludeUserID.
// Group subjects are matched against BOTH directory groups (by name→id→members)
// and AD groups (by name), since a RACI group name may be either. Everyone is
// ignored. Best-effort: a failed lookup for one subject is skipped, not fatal.
func (r Resolver) Recipients(ctx context.Context, subjects []*notifyv1.InformedSubject, excludeUserID string) ([]string, error) {
	set := map[string]bool{}
	var groupNames []string
	for _, s := range subjects {
		switch s.GetKind() {
		case "user":
			set[s.GetName()] = true
		case "group":
			groupNames = append(groupNames, s.GetName())
		}
	}
	if len(groupNames) > 0 && r.Identity != nil {
		r.addDirectoryGroupMembers(ctx, groupNames, set)
		r.addADGroupMembers(ctx, groupNames, set)
	}
	delete(set, excludeUserID)
	delete(set, "")
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out, nil
}

// addDirectoryGroupMembers adds the members of each named directory group
// (name -> id -> members) to set. A failed lookup is skipped.
func (r Resolver) addDirectoryGroupMembers(ctx context.Context, groupNames []string, set map[string]bool) {
	gr, err := r.Identity.ListGroups(ctx, &identityv1.ListGroupsRequest{})
	if err != nil {
		return
	}
	nameToID := map[string]string{}
	for _, g := range gr.GetGroups() {
		nameToID[g.GetName()] = g.GetId()
	}
	for _, n := range groupNames {
		id, ok := nameToID[n]
		if !ok {
			continue
		}
		if mr, err := r.Identity.ListGroupMembers(ctx, &identityv1.ListGroupMembersRequest{GroupId: id}); err == nil {
			for _, u := range mr.GetUsers() {
				set[u.GetId()] = true
			}
		}
	}
}

// addADGroupMembers adds the users of each named AD group to set. A failed
// lookup is skipped.
func (r Resolver) addADGroupMembers(ctx context.Context, groupNames []string, set map[string]bool) {
	if ar, err := r.Identity.ListUsersByAdGroups(ctx, &identityv1.ListUsersByAdGroupsRequest{Names: groupNames}); err == nil {
		for _, u := range ar.GetUsers() {
			set[u.GetId()] = true
		}
	}
}

// ActorLabel returns a display name for userID (falls back to the id).
func (r Resolver) ActorLabel(ctx context.Context, userID string) string {
	if r.Identity == nil {
		return userID
	}
	resp, err := r.Identity.ResolveUserLabels(ctx, &identityv1.ResolveUserLabelsRequest{Ids: []string{userID}})
	if err != nil || len(resp.GetLabels()) == 0 || resp.GetLabels()[0].GetName() == "" {
		return userID
	}
	return resp.GetLabels()[0].GetName()
}
