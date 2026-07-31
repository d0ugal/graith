package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestDaemonTracingDisabledNoop(t *testing.T) {
	previous := otel.GetTracerProvider()

	otel.SetTracerProvider(noop.NewTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	ctx, span := startDaemonSpan(t.Context(), "graith.session.create",
		attribute.Bool("graith.session.no_repo", true),
	)

	if span.IsRecording() {
		t.Fatal("span records with noop tracing provider")
	}

	if trace.SpanFromContext(ctx).IsRecording() {
		t.Fatal("context span records with noop tracing provider")
	}

	endDaemonSpan(span, errors.New("dreich disabled tracing error"))
}

func TestAcquireLaunchSlotTracesQueueWait(t *testing.T) {
	recorder := installDaemonTestTracer(t)

	sm := newLaunchTestSM(t, config.LaunchConfig{MaxConcurrent: 2})

	slot, err := sm.acquireLaunchSlot(t.Context(), "sensitive-id", "private-name")
	if err != nil {
		t.Fatalf("acquireLaunchSlot() error = %v", err)
	}

	slot.release()

	span := singleEndedSpan(t, recorder)
	if got := span.Name(); got != "graith.session.launch.slot_wait" {
		t.Fatalf("span name = %q, want graith.session.launch.slot_wait", got)
	}

	assertSpanAttr(t, span, "graith.launch.throttle_configured", true)
	assertSpanAttr(t, span, "graith.launch.inflight", int64(1))
	assertSpanAttr(t, span, "graith.launch.capacity", int64(2))
	assertSpanAttr(t, span, "graith.launch.queued", int64(1))

	assertSpanAvoidsText(t, span, "sensitive-id")
	assertSpanAvoidsText(t, span, "private-name")
}

func TestDaemonSpanErrorUsesSanitizedType(t *testing.T) {
	recorder := installDaemonTestTracer(t)

	_, span := startDaemonSpan(t.Context(), "graith.session.launch.spawn")
	endDaemonSpan(span, fmt.Errorf("open /Users/dougal/private-token: %w", os.ErrPermission))

	got := singleEndedSpan(t, recorder)
	assertSpanAttr(t, got, "error.type", "fs.permission")
	assertSpanAvoidsText(t, got, "/Users/dougal/private-token")

	if events := got.Events(); len(events) != 0 {
		t.Fatalf("span recorded %d event(s), want none so raw error messages are not exported", len(events))
	}

	if status := got.Status(); status.Code != codes.Error || status.Description != "fs.permission" {
		t.Fatalf("span status = (%v, %q), want error fs.permission", status.Code, status.Description)
	}
}

func TestDaemonSpanFailureCountMarksPartialFailure(t *testing.T) {
	recorder := installDaemonTestTracer(t)

	_, span := startDaemonSpan(t.Context(), "graith.session.soft_delete.purge_expired")
	endDaemonSpanWithFailures(span, nil, 2)

	got := singleEndedSpan(t, recorder)
	assertSpanAttr(t, got, "error.type", "operation.partial_failure")

	if status := got.Status(); status.Code != codes.Error || status.Description != "operation.partial_failure" {
		t.Fatalf("span status = (%v, %q), want error operation.partial_failure", status.Code, status.Description)
	}
}

func installDaemonTestTracer(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)

	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())

		otel.SetTracerProvider(previous)
	})

	return recorder
}

func singleEndedSpan(t *testing.T, recorder *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}

	return spans[0]
}

func assertSpanAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string, want any) {
	t.Helper()

	got, ok := findSpanAttr(span, key)
	if !ok {
		t.Fatalf("span missing attribute %q", key)
	}

	switch want := want.(type) {
	case bool:
		if got.AsBool() != want {
			t.Fatalf("attribute %q = %v, want %v", key, got.AsBool(), want)
		}
	case int64:
		if got.AsInt64() != want {
			t.Fatalf("attribute %q = %v, want %v", key, got.AsInt64(), want)
		}
	case string:
		if got.AsString() != want {
			t.Fatalf("attribute %q = %q, want %q", key, got.AsString(), want)
		}
	default:
		t.Fatalf("unsupported attribute assertion type %T", want)
	}
}

func findSpanAttr(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value, true
		}
	}

	return attribute.Value{}, false
}

func assertSpanAvoidsText(t *testing.T, span sdktrace.ReadOnlySpan, forbidden string) {
	t.Helper()

	if strings.Contains(span.Name(), forbidden) {
		t.Fatalf("span name %q contains forbidden text %q", span.Name(), forbidden)
	}

	for _, attr := range span.Attributes() {
		if strings.Contains(string(attr.Key), forbidden) {
			t.Fatalf("span attribute key %q contains forbidden text %q", attr.Key, forbidden)
		}

		if strings.Contains(attr.Value.AsString(), forbidden) {
			t.Fatalf("span attribute %q value contains forbidden text %q", attr.Key, forbidden)
		}
	}

	for _, event := range span.Events() {
		if strings.Contains(event.Name, forbidden) {
			t.Fatalf("span event %q contains forbidden text %q", event.Name, forbidden)
		}

		for _, attr := range event.Attributes {
			if strings.Contains(attr.Value.AsString(), forbidden) {
				t.Fatalf("span event attribute %q contains forbidden text %q", attr.Key, forbidden)
			}
		}
	}
}
