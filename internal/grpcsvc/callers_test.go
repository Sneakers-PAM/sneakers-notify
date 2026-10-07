// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"maps"
	"net"
	"testing"

	log "github.com/Bugs5382/go-log"
	workloadauth "github.com/Bugs5382/go-workload-identity"
	notifyv1 "github.com/Sneakers-PAM/sneakers-notify/gen/go/sneakers/notify/v1"
	"github.com/Sneakers-PAM/sneakers-notify/internal/fanout"
	"github.com/Sneakers-PAM/sneakers-notify/internal/server"
	"github.com/Sneakers-PAM/sneakers-notify/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// TestCallerPolicyPerMethod pins the allow-list of every notify method: the
// vault sends events as itself; the gateway reads and marks the signed-in
// user's inbox on their behalf.
func TestCallerPolicyPerMethod(t *testing.T) {
	gw := map[string]workloadauth.Access{CallerGateway: workloadauth.OnBehalf}
	want := map[string]map[string]workloadauth.Access{
		notifyv1.NotifyService_NotifyEvent_FullMethodName:       {CallerVault: workloadauth.Self},
		notifyv1.NotifyService_ListNotifications_FullMethodName: gw,
		notifyv1.NotifyService_UnreadCount_FullMethodName:       gw,
		notifyv1.NotifyService_MarkRead_FullMethodName:          gw,
		notifyv1.NotifyService_MarkAllRead_FullMethodName:       gw,
	}
	p := CallerPolicy()
	desc := notifyv1.NotifyService_ServiceDesc
	if len(desc.Streams) != 0 {
		t.Fatalf("notify has streaming methods now; give them an allow-list: %v", desc.Streams)
	}
	if len(p) != len(desc.Methods) || len(want) != len(desc.Methods) {
		t.Fatalf("policy covers %d methods, the test %d, the service has %d", len(p), len(want), len(desc.Methods))
	}
	for _, md := range desc.Methods {
		full := "/" + desc.ServiceName + "/" + md.MethodName
		if got := p[full]; !maps.Equal(got, want[full]) {
			t.Errorf("%s: allow-list %v, want %v", md.MethodName, got, want[full])
		}
		for _, c := range []string{"mcp", "workflow", "sshbroker", "connector", "identity", "audit", "notify"} {
			if _, ok := p.Lookup(full, c); ok {
				t.Errorf("%s: %s must not be allowed", md.MethodName, c)
			}
		}
	}
}

const authNS = "sneakers"

type authFixture struct {
	st     *memStore
	iss    *testIssuer
	client notifyv1.NotifyServiceClient
	health healthpb.HealthClient
}

// newAuthFixture serves notify behind the real workload-auth interceptors and
// the real verifier, over a gRPC connection. The verifier's service-account
// list is the chart's for notify plus mcp, so mcp shows what a listed caller
// outside the method policy gets.
func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	iss := newTestIssuer(t)
	var allowed []string
	for _, c := range []string{"vault", "gateway", "mcp"} {
		allowed = append(allowed, authNS+"/sneakers-"+c)
	}
	v, err := workloadauth.NewVerifier(workloadauth.Config{
		Issuer: iss.URL, CAFile: iss.CAFile, AllowedServiceAccounts: allowed,
		Audience: server.WorkloadAudience, ServiceAccountPrefix: server.WorkloadServiceAccountPrefix,
	}, log.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := &memStore{items: map[string][]store.Item{}}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(
		grpc.ChainUnaryInterceptor(workloadauth.UnaryServerInterceptor(v, CallerPolicy(), log.Nop())),
		grpc.ChainStreamInterceptor(workloadauth.StreamServerInterceptor(v, CallerPolicy(), log.Nop())),
	)
	RegisterServer(gs, New(st, fanout.Resolver{}))
	healthpb.RegisterHealthServer(gs, health.NewServer())
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &authFixture{st: st, iss: iss, client: notifyv1.NewNotifyServiceClient(conn), health: healthpb.NewHealthClient(conn)}
}

// as returns a context carrying a valid token for the service account
// sneakers-<caller>.
func (f *authFixture) as(t *testing.T, caller string) context.Context {
	t.Helper()
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+f.iss.token(t, authNS, "sneakers-"+caller))
}

func (f *authFixture) notify(ctx context.Context) error {
	_, err := f.client.NotifyEvent(ctx, &notifyv1.NotifyEventRequest{
		Action: "secret.reveal", ResourceKind: "secret", ResourceId: "sec-1", ActorUserId: "user-alice",
		InformedSubjects: []*notifyv1.InformedSubject{{Kind: "user", Name: "user-bob"}},
	})
	return err
}

func (f *authFixture) inbox(ctx context.Context) error {
	_, err := f.client.ListNotifications(ctx, &notifyv1.ListNotificationsRequest{UserId: "user-bob"})
	return err
}

func wantCode(t *testing.T, what string, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("%s: %v, want %s", what, err, want)
	}
}

func TestCallerAuth_UnlistedPodIsRefusedEvenWithAValidToken(t *testing.T) {
	f := newAuthFixture(t)
	// A correctly signed token for a service account that isn't on the list.
	wantCode(t, "workflow NotifyEvent", f.notify(f.as(t, "workflow")), codes.Unauthenticated)
	wantCode(t, "identity ListNotifications", f.inbox(f.as(t, "identity")), codes.Unauthenticated)
	// On the list, but in no method's policy.
	wantCode(t, "mcp NotifyEvent", f.notify(f.as(t, "mcp")), codes.PermissionDenied)
	wantCode(t, "mcp ListNotifications", f.inbox(f.as(t, "mcp")), codes.PermissionDenied)
	if n := len(f.st.items["user-bob"]); n != 0 {
		t.Fatalf("refused calls wrote %d inbox items", n)
	}
}

func TestCallerAuth_NoTokenIsUnauthenticated(t *testing.T) {
	f := newAuthFixture(t)
	wantCode(t, "no token", f.inbox(context.Background()), codes.Unauthenticated)
	bad := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer not-a-token")
	wantCode(t, "bad token", f.notify(bad), codes.Unauthenticated)
}

func TestCallerAuth_VaultSendsAndTheGatewayReads(t *testing.T) {
	f := newAuthFixture(t)
	if err := f.notify(f.as(t, "vault")); err != nil {
		t.Fatalf("vault NotifyEvent: %v", err)
	}
	gw := f.as(t, "gateway")
	list, err := f.client.ListNotifications(gw, &notifyv1.ListNotificationsRequest{UserId: "user-bob"})
	if err != nil || len(list.GetNotifications()) != 1 {
		t.Fatalf("gateway ListNotifications: %v %v", list, err)
	}
	if _, err := f.client.UnreadCount(gw, &notifyv1.UnreadCountRequest{UserId: "user-bob"}); err != nil {
		t.Fatalf("gateway UnreadCount: %v", err)
	}
	if _, err := f.client.MarkRead(gw, &notifyv1.MarkReadRequest{UserId: "user-bob", Id: list.GetNotifications()[0].GetId()}); err != nil {
		t.Fatalf("gateway MarkRead: %v", err)
	}
	if _, err := f.client.MarkAllRead(gw, &notifyv1.MarkAllReadRequest{UserId: "user-bob"}); err != nil {
		t.Fatalf("gateway MarkAllRead: %v", err)
	}
}

func TestCallerAuth_EachCallerOnlyOnItsOwnMethods(t *testing.T) {
	f := newAuthFixture(t)
	wantCode(t, "gateway NotifyEvent", f.notify(f.as(t, "gateway")), codes.PermissionDenied)
	vault := f.as(t, "vault")
	wantCode(t, "vault ListNotifications", f.inbox(vault), codes.PermissionDenied)
	_, err := f.client.UnreadCount(vault, &notifyv1.UnreadCountRequest{UserId: "user-bob"})
	wantCode(t, "vault UnreadCount", err, codes.PermissionDenied)
	_, err = f.client.MarkRead(vault, &notifyv1.MarkReadRequest{UserId: "user-bob", Id: "x"})
	wantCode(t, "vault MarkRead", err, codes.PermissionDenied)
	_, err = f.client.MarkAllRead(vault, &notifyv1.MarkAllReadRequest{UserId: "user-bob"})
	wantCode(t, "vault MarkAllRead", err, codes.PermissionDenied)
}

func TestCallerAuth_HealthNeedsNoToken(t *testing.T) {
	f := newAuthFixture(t)
	if _, err := f.health.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("health without a token: %v", err)
	}
}
