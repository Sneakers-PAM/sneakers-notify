// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

func TestEnvOTLPEndpointDefaultsToEmpty(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	if got := env("OTEL_EXPORTER_OTLP_ENDPOINT", ""); got != "" {
		t.Fatalf("default OTLP endpoint got %q, want empty (no collector)", got)
	}
}

func TestEnvOTLPEndpointHonorsSet(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "collector.example.org:4317")
	if got := env("OTEL_EXPORTER_OTLP_ENDPOINT", ""); got != "collector.example.org:4317" {
		t.Fatalf("got %q", got)
	}
}
