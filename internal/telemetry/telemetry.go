// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

// Package telemetry holds what notify adds to go-otel: the two export
// switches, the allow-lists for what may be exported and the optional otlp
// health dependency.
package telemetry

import (
	"context"
	"errors"
	"io"
	"os"
	"regexp"

	"github.com/Bugs5382/go-buildinfo/health"
	otel "github.com/Bugs5382/go-otel"
)

// The switches take the exact value "true"; anything else leaves them off.
const (
	EnvEndpoint           = "OTEL_EXPORTER_OTLP_ENDPOINT"
	EnvInsecureSkipVerify = "OTLP_INSECURE_SKIP_VERIFY"
	EnvLogsEnabled        = "OTLP_LOGS_ENABLED"
)

// allowedLogFields is every log field that may leave the process, from
// notify and from the go-buildinfo and go-workload-identity logging it runs.
// outcome looks like a short enum but go-workload-identity puts error text in
// it. A new field exports nothing until it's added here, and only ids, counts,
// durations, classes, states and short enums belong here.
var allowedLogFields = []string{
	"attempt",
	"attempts",
	"bearer_file",
	"ca_file",
	"code",
	"component",
	"dependencies",
	"dependency",
	"duration_ms",
	"env",
	"error_class",
	"from",
	"jwks_override",
	"keys",
	"kept_keys",
	"method",
	"otlp_insecure_skip_verify",
	"otlp_logs",
	"port",
	"required",
	"retry_in",
	"retry_in_ms",
	"to",
}

// rpcMethod is the shape of a real notify or identity method. otelgrpc copies
// whatever method a client sent into span names and rpc.method.
var rpcMethod = regexp.MustCompile(`^sneakers\.[a-z]+\.v1\.[A-Za-z]+/[A-Za-z]+$`)

// exportedAttributes is every span and metric attribute notify sends. No
// resource attribute is named, so only service.name leaves.
var exportedAttributes = []string{"rpc.method", "rpc.response.status_code"}

var apiKeyPattern = regexp.MustCompile(`\bsnk_[A-Za-z0-9_\-]+`)

// errExportFailing is all the health report says: the detail in
// otel.ExportState can carry the collector's own text.
var errExportFailing = errors.New("otlp export failing")

// AllowedLogFields returns a copy of the exported log field names.
func AllowedLogFields() []string {
	return append([]string(nil), allowedLogFields...)
}

// Settings is the export configuration read from the environment.
type Settings struct {
	Endpoint           string
	InsecureSkipVerify bool
	Logs               bool
}

// FromEnv reads the settings through getenv.
func FromEnv(getenv func(string) string) Settings {
	return Settings{
		Endpoint:           getenv(EnvEndpoint),
		InsecureSkipVerify: getenv(EnvInsecureSkipVerify) == "true",
		Logs:               getenv(EnvLogsEnabled) == "true",
	}
}

// Output is where the service's loggers write: stdout, plus the OTLP log
// writer when logs are on. The writer discards everything until Init, so a
// logger can be built first.
func (s Settings) Output() io.Writer {
	if !s.Logs {
		return os.Stdout
	}
	return io.MultiWriter(os.Stdout, otel.LogWriter())
}

// Options are the go-otel options for these settings.
func (s Settings) Options() []otel.Option {
	return []otel.Option{
		otel.WithInsecureSkipVerify(s.InsecureSkipVerify),
		otel.WithLogs(s.Logs),
		otel.WithLogAllowFields(allowedLogFields...),
		otel.WithLogRedactPatterns(apiKeyPattern),
		otel.WithTelemetryFilter(true),
		otel.WithTraceAllowSpanNames(rpcMethod),
		otel.WithTraceAllowAttributes(exportedAttributes...),
		otel.WithMetricAllowAttributes(exportedAttributes...),
		otel.WithAttributeValuePattern("rpc.method", rpcMethod),
		otel.WithBaggage(false),
		otel.WithRedactedErrors(true),
	}
}

// Dependency is the optional otlp health dependency, false when there's no
// endpoint to report on. It never fails readiness.
func (s Settings) Dependency() (health.Dependency, bool) {
	if s.Endpoint == "" {
		return health.Dependency{}, false
	}
	return health.Dependency{Name: "otlp", Check: func(context.Context) error {
		if ExportDown(otel.ExportState(), s.Logs) {
			return health.Classify(errExportFailing, "unavailable")
		}
		return nil
	}}, true
}

// ExportDown reports whether the last export of a signal in use failed. Logs
// count only when they're on and go-otel hasn't kept them off.
func ExportDown(st otel.ExportStatus, logs bool) bool {
	if failing(st.Traces) || failing(st.Metrics) {
		return true
	}
	return logs && st.Logs.Disabled == "" && failing(st.Logs)
}

func failing(s otel.SignalStatus) bool {
	return s.LastErrorAt.After(s.LastSuccess)
}
