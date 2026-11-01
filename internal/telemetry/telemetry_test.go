// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package telemetry_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bugs5382/go-buildinfo/health"
	log "github.com/Bugs5382/go-log"
	otel "github.com/Bugs5382/go-otel"
	"github.com/Sneakers-PAM/sneakers-notify/internal/telemetry"
	otelapi "go.opentelemetry.io/otel"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

const canary = "canary-7f3a9c"

// collector is an in-process OTLP receiver. A non-nil refuse fails every
// trace export with that error.
type collector struct {
	collogs.UnimplementedLogsServiceServer
	mu     sync.Mutex
	logs   strings.Builder
	refuse error
}

func (c *collector) Export(_ context.Context, r *collogs.ExportLogsServiceRequest) (*collogs.ExportLogsServiceResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := protojson.Marshal(r)
	if err != nil {
		return nil, err
	}
	c.logs.Write(b)
	return &collogs.ExportLogsServiceResponse{}, nil
}

func (c *collector) received() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logs.String()
}

type traceReceiver struct {
	coltrace.UnimplementedTraceServiceServer
	c *collector
}

func (t traceReceiver) Export(context.Context, *coltrace.ExportTraceServiceRequest) (*coltrace.ExportTraceServiceResponse, error) {
	if t.c.refuse != nil {
		return nil, t.c.refuse
	}
	return &coltrace.ExportTraceServiceResponse{}, nil
}

func startCollector(t *testing.T, c *collector) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	collogs.RegisterLogsServiceServer(s, c)
	coltrace.RegisterTraceServiceServer(s, traceReceiver{c: c})
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return "http://" + lis.Addr().String()
}

func getenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnv_UnsetChangesNothing(t *testing.T) {
	s := telemetry.FromEnv(getenv(nil))
	if s.Endpoint != "" || s.InsecureSkipVerify || s.Logs {
		t.Fatalf("got %+v, want the zero settings", s)
	}
	if s.Output() != io.Writer(os.Stdout) {
		t.Fatal("logs must go to plain stdout when export is off")
	}
	if _, ok := s.Dependency(); ok {
		t.Fatal("no endpoint must mean no otlp dependency")
	}
}

func TestFromEnv_OnlyTrueSwitchesOn(t *testing.T) {
	for _, v := range []string{"", "1", "yes", "TRUE", "True", "false"} {
		s := telemetry.FromEnv(getenv(map[string]string{telemetry.EnvInsecureSkipVerify: v, telemetry.EnvLogsEnabled: v}))
		if s.InsecureSkipVerify || s.Logs {
			t.Fatalf("%q switched a setting on: %+v", v, s)
		}
	}
	s := telemetry.FromEnv(getenv(map[string]string{telemetry.EnvInsecureSkipVerify: "true", telemetry.EnvLogsEnabled: "true"}))
	if !s.InsecureSkipVerify || !s.Logs {
		t.Fatalf("got %+v, want both on", s)
	}
}

func TestDependency_AppearsOnlyWithAnEndpointAndIsOptional(t *testing.T) {
	dep, ok := telemetry.Settings{Endpoint: "https://collector.example.org:4317"}.Dependency()
	if !ok {
		t.Fatal("want a dependency with an endpoint set")
	}
	if dep.Name != "otlp" || dep.Required {
		t.Fatalf("got %q required=%v, want optional otlp", dep.Name, dep.Required)
	}
}

func TestExportDown(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	failing := otel.SignalStatus{LastSuccess: t0, LastErrorAt: t0.Add(time.Second)}
	healthy := otel.SignalStatus{LastSuccess: t0.Add(time.Second), LastErrorAt: t0}
	cases := []struct {
		name string
		st   otel.ExportStatus
		logs bool
		want bool
	}{
		{"nothing yet", otel.ExportStatus{}, false, false},
		{"recovered", otel.ExportStatus{Traces: healthy, Metrics: healthy}, false, false},
		{"traces", otel.ExportStatus{Traces: failing}, false, true},
		{"metrics", otel.ExportStatus{Metrics: failing}, false, true},
		{"logs off", otel.ExportStatus{Logs: failing}, false, false},
		{"logs on", otel.ExportStatus{Logs: failing}, true, true},
		{"logs kept off by skip verify", otel.ExportStatus{Logs: otel.SignalStatus{LastErrorAt: t0, Disabled: "skip verify"}}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := telemetry.ExportDown(c.st, c.logs); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestFailingCollectorDegradesAndDoesNotFailReadiness(t *testing.T) {
	c := &collector{refuse: status.Error(codes.PermissionDenied, "no")}
	s := telemetry.Settings{Endpoint: startCollector(t, c)}
	ctx := context.Background()
	shutdown, err := otel.InitWithOptions(ctx, "notify-test", s.Endpoint, s.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	_, span := otelapi.Tracer("test").Start(ctx, "op")
	span.End()
	_ = shutdown(ctx)

	dep, ok := s.Dependency()
	if !ok {
		t.Fatal("want the dependency")
	}
	checker := health.New()
	if err := checker.Register(dep); err != nil {
		t.Fatal(err)
	}
	rep := checker.Report(ctx)
	if !rep.Ready || rep.Status != health.StateDegraded {
		t.Fatalf("got %+v, want ready and degraded", rep)
	}
	if len(rep.Dependencies) != 1 || rep.Dependencies[0].State != health.StateDegraded {
		t.Fatalf("got %+v, want otlp degraded", rep.Dependencies)
	}
	if strings.Contains(rep.Dependencies[0].Error, "127.0.0.1") {
		t.Fatalf("report carries the endpoint: %q", rep.Dependencies[0].Error)
	}
}

func TestCanaryInAnUnallowedFieldNeverReachesTheCollector(t *testing.T) {
	c := &collector{}
	s := telemetry.Settings{Endpoint: startCollector(t, c), Logs: true}
	lg := log.NewLoggerWithOptions("notify-test", log.WithOutput(s.Output()))
	ctx := context.Background()
	shutdown, err := otel.InitWithOptions(ctx, "notify-test", s.Endpoint, s.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	lg.Warn("dependency not reachable yet; retrying",
		log.F("dependency", "valkey"), log.F("attempt", 3),
		log.F("error", canary), log.F("issuer", canary), log.F("outcome", canary), log.F("reason", canary))
	lg.Warn("key snk_abc-123_def in a message")
	lg.Warn("level floor", log.F("attempt", 4))
	lg.Debug("debug stays home " + canary)
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	got := c.received()
	if !strings.Contains(got, "valkey") {
		t.Fatalf("the allowed field did not arrive: %s", got)
	}
	if strings.Contains(got, canary) {
		t.Fatalf("canary reached the collector: %s", got)
	}
	if strings.Contains(got, "snk_abc") {
		t.Fatalf("an snk_ key reached the collector: %s", got)
	}
}

// The fields that can hold free text stay off the list whatever else is added.
func TestAllowListLeavesOutFreeText(t *testing.T) {
	for _, f := range []string{
		"error", "panic", "stack", "issuer", "audience", "allowed_serviceaccounts", "identity",
		"jwks_url", "target", "outcome", "reason", "caller", "service_account",
	} {
		if slices.Contains(telemetry.AllowedLogFields(), f) {
			t.Errorf("%s must not be exported", f)
		}
	}
}

func TestErrorHandler_LeavesTheEndpointOut(t *testing.T) {
	var buf strings.Builder
	lg := log.NewLoggerWithOptions("notify-test", log.WithOutput(&buf))
	telemetry.SetErrorHandler(lg)
	otelapi.Handle(errors.New("export to https://collector.example.org:4317 failed"))
	otelapi.Handle(status.Error(codes.Unauthenticated, "authorization=abc"))
	out := buf.String()
	for _, leak := range []string{"collector.example.org", "authorization"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q leaked: %s", leak, out)
		}
	}
	if !strings.Contains(out, `"error_class":"unauthenticated"`) {
		t.Fatalf("want the class logged: %s", out)
	}
}
