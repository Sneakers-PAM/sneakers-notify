// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	workloadauth "github.com/Bugs5382/go-workload-identity"
	notifyv1 "github.com/Sneakers-PAM/sneakers-notify/gen/go/sneakers/notify/v1"
)

// Caller names, from the service accounts sneakers-<name>.
const (
	CallerVault   = "vault"
	CallerGateway = "gateway"
)

// inboxMethods serve the signed-in user's inbox through the gateway.
var inboxMethods = []string{
	notifyv1.NotifyService_ListNotifications_FullMethodName,
	notifyv1.NotifyService_UnreadCount_FullMethodName,
	notifyv1.NotifyService_MarkRead_FullMethodName,
	notifyv1.NotifyService_MarkAllRead_FullMethodName,
}

// CallerPolicy is notify's per-method allow-list: the vault sends events as
// itself, and the gateway reads and marks the inbox of the user_id it passes,
// on behalf of the signed-in user. Anything else is refused.
func CallerPolicy() workloadauth.Policy {
	p := workloadauth.Policy{
		notifyv1.NotifyService_NotifyEvent_FullMethodName: {CallerVault: workloadauth.Self},
	}
	for _, m := range inboxMethods {
		p[m] = map[string]workloadauth.Access{CallerGateway: workloadauth.OnBehalf}
	}
	return p
}
