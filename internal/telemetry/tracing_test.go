package telemetry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestTracerIsSafeWithoutStartedRuntime(t *testing.T) {
	otel.SetTracerProvider(noop.NewTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	tracer := Tracer("braw")
	if tracer == nil {
		t.Fatal("Tracer() returned nil")
	}

	_, span := tracer.Start(t.Context(), "canny")
	if span == nil {
		t.Fatal("Start() returned nil span")
	}
	defer span.End()

	if span.SpanContext().IsValid() {
		t.Fatal("disabled tracer produced a valid recording span")
	}
}

func TestStartTracingPassesExplicitOptions(t *testing.T) {
	tests := map[string]struct {
		protocol string
		endpoint string
		insecure bool
	}{
		"grpc": {
			protocol: ProtocolGRPC,
			endpoint: "127.0.0.1:4317",
			insecure: true,
		},
		"http": {
			protocol: ProtocolHTTPProtobuf,
			endpoint: "http://127.0.0.1:4318/v1/traces",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var got TracingOptions

			oldExporter := newTraceExporter
			newTraceExporter = func(_ context.Context, opts TracingOptions) (sdktrace.SpanExporter, error) {
				got = opts

				return &noopSpanExporter{}, nil
			}

			t.Cleanup(func() {
				newTraceExporter = oldExporter

				otel.SetTracerProvider(noop.NewTracerProvider())
			})

			rt, err := StartTracing(t.Context(), TracingOptions{
				Endpoint: test.endpoint,
				Protocol: test.protocol,
				Insecure: test.insecure,
				Timeout:  25 * time.Millisecond,
				Headers: map[string]string{
					"authorization": "Bearer braw",
				},
			})
			if err != nil {
				t.Fatalf("StartTracing() error = %v", err)
			}

			t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })

			if got.Endpoint != test.endpoint {
				t.Errorf("Endpoint = %q, want %q", got.Endpoint, test.endpoint)
			}

			if got.Protocol != test.protocol {
				t.Errorf("Protocol = %q, want %q", got.Protocol, test.protocol)
			}

			if got.Insecure != test.insecure {
				t.Errorf("Insecure = %v, want %v", got.Insecure, test.insecure)
			}

			if got.Timeout != 25*time.Millisecond {
				t.Errorf("Timeout = %v, want 25ms", got.Timeout)
			}

			if got.Headers["authorization"] != "Bearer braw" {
				t.Errorf("Headers not passed through: %#v", got.Headers)
			}
		})
	}
}

func TestStartTracingRejectsMissingEndpointBeforeExporter(t *testing.T) {
	oldExporter := newTraceExporter
	called := false
	newTraceExporter = func(context.Context, TracingOptions) (sdktrace.SpanExporter, error) {
		called = true

		return nil, errors.New("unexpected exporter")
	}

	t.Cleanup(func() { newTraceExporter = oldExporter })

	_, err := StartTracing(t.Context(), TracingOptions{Protocol: ProtocolGRPC})
	if err == nil {
		t.Fatal("StartTracing() error = nil, want endpoint error")
	}

	if called {
		t.Fatal("missing endpoint constructed exporter")
	}
}

func TestHTTPExporterIgnoresOTLPEnvironment(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1/wrong")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:1/wrong-traces")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-global=bad")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "x-env=bad,x-graith=env")
	t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "gzip")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_COMPRESSION", "gzip")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")

	type traceRequest struct {
		path            string
		configHeader    string
		envHeader       string
		globalHeader    string
		contentEncoding string
	}

	requests := make(chan traceRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests <- traceRequest{
			path:            r.URL.Path,
			configHeader:    r.Header.Get("x-graith"),
			envHeader:       r.Header.Get("x-env"),
			globalHeader:    r.Header.Get("x-global"),
			contentEncoding: r.Header.Get("content-encoding"),
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	rt, err := StartTracing(t.Context(), TracingOptions{
		Endpoint: server.URL + "/canny/traces",
		Protocol: ProtocolHTTPProtobuf,
		Timeout:  500 * time.Millisecond,
		Headers: map[string]string{
			"x-graith": "config",
		},
	})
	if err != nil {
		t.Fatalf("StartTracing() error = %v", err)
	}

	_, span := Tracer("braw").Start(t.Context(), "blether")
	span.End()

	if err := rt.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	select {
	case got := <-requests:
		if got.path != "/canny/traces" {
			t.Errorf("request path = %q, want /canny/traces", got.path)
		}

		if got.configHeader != "config" {
			t.Errorf("x-graith header = %q, want config", got.configHeader)
		}

		if got.envHeader != "" {
			t.Errorf("x-env header = %q, want empty", got.envHeader)
		}

		if got.globalHeader != "" {
			t.Errorf("x-global header = %q, want empty", got.globalHeader)
		}

		if got.contentEncoding != "" {
			t.Errorf("content-encoding = %q, want empty", got.contentEncoding)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OTLP HTTP request")
	}
}

func TestGRPCExporterIgnoresOTLPEnvironmentAndDialsLazily(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "127.0.0.1:1")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "127.0.0.1:1")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-global=bad")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "x-env=bad,x-graith=env")
	t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "gzip")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_COMPRESSION", "gzip")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}

	recordingListener := &recordingListener{
		Listener: listener,
		accepts:  make(chan struct{}, 1),
	}
	service := &recordingTraceService{
		requests: make(chan metadata.MD, 1),
	}
	server := grpc.NewServer()
	collectortracepb.RegisterTraceServiceServer(server, service)

	go func() { _ = server.Serve(recordingListener) }()

	t.Cleanup(server.Stop)

	rt, err := StartTracing(t.Context(), TracingOptions{
		Endpoint: listener.Addr().String(),
		Protocol: ProtocolGRPC,
		Insecure: true,
		Timeout:  500 * time.Millisecond,
		Headers: map[string]string{
			"x-graith": "config",
		},
	})
	if err != nil {
		t.Fatalf("StartTracing() error = %v", err)
	}

	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })

	select {
	case <-recordingListener.accepts:
		t.Fatal("StartTracing() dialed OTLP gRPC endpoint before a span was exported")
	case <-time.After(100 * time.Millisecond):
	}

	_, span := Tracer("braw").Start(t.Context(), "blether")
	span.End()

	if err := rt.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	select {
	case got := <-service.requests:
		if values := got.Get("x-graith"); len(values) != 1 || values[0] != "config" {
			t.Errorf("x-graith metadata = %v, want [config]", values)
		}

		if values := got.Get("x-env"); len(values) != 0 {
			t.Errorf("x-env metadata = %v, want empty", values)
		}

		if values := got.Get("x-global"); len(values) != 0 {
			t.Errorf("x-global metadata = %v, want empty", values)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OTLP gRPC request")
	}
}

func TestStartTracingResourceAttributes(t *testing.T) {
	var got *resource.Resource

	oldExporter := newTraceExporter
	oldProvider := newTraceProvider
	newTraceExporter = func(context.Context, TracingOptions) (sdktrace.SpanExporter, error) {
		return &noopSpanExporter{}, nil
	}
	newTraceProvider = func(exporter sdktrace.SpanExporter, res *resource.Resource, _ time.Duration) *sdktrace.TracerProvider {
		got = res

		return sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithResource(res))
	}

	t.Cleanup(func() {
		newTraceExporter = oldExporter
		newTraceProvider = oldProvider

		otel.SetTracerProvider(noop.NewTracerProvider())
	})

	rt, err := StartTracing(t.Context(), TracingOptions{
		Endpoint: "127.0.0.1:4317",
		Protocol: ProtocolGRPC,
		Resource: ResourceOptions{
			ServiceVersion:        "v1.2.3",
			Commit:                "abc123",
			DaemonInstanceID:      "bothy-instance",
			Profile:               "canny",
			ProcessPID:            123,
			ProcessExecutableName: "gr",
		},
	})
	if err != nil {
		t.Fatalf("StartTracing() error = %v", err)
	}

	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })

	assertResourceAttr(t, got, "service.name", ServiceNameDefault)
	assertResourceAttr(t, got, "service.version", "v1.2.3")
	assertResourceAttr(t, got, "service.instance.id", "bothy-instance")
	assertResourceAttr(t, got, "graith.daemon.instance_id", "bothy-instance")
	assertResourceAttr(t, got, "graith.commit", "abc123")
	assertResourceAttr(t, got, "graith.profile", "canny")
	assertResourceAttr(t, got, "graith.process.kind", "daemon")
	assertResourceAttr(t, got, "process.pid", int64(123))
	assertResourceAttr(t, got, "process.executable.name", "gr")
}

func TestShutdownIsBounded(t *testing.T) {
	oldExporter := newTraceExporter
	newTraceExporter = func(context.Context, TracingOptions) (sdktrace.SpanExporter, error) {
		return blockingShutdownExporter{}, nil
	}

	t.Cleanup(func() {
		newTraceExporter = oldExporter

		otel.SetTracerProvider(noop.NewTracerProvider())
	})

	rt, err := StartTracing(t.Context(), TracingOptions{
		Endpoint: "127.0.0.1:4317",
		Protocol: ProtocolGRPC,
		Timeout:  10 * time.Millisecond,
		Logger:   slog.Default(),
	})
	if err != nil {
		t.Fatalf("StartTracing() error = %v", err)
	}

	started := time.Now()
	err = rt.Shutdown(context.Background())
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("Shutdown() error = nil, want context deadline from blocking exporter")
	}

	if elapsed > 250*time.Millisecond {
		t.Fatalf("Shutdown() took %v, want bounded timeout", elapsed)
	}
}

type noopSpanExporter struct{}

func (*noopSpanExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (*noopSpanExporter) Shutdown(context.Context) error                             { return nil }

type blockingShutdownExporter struct{}

func (blockingShutdownExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return nil
}
func (blockingShutdownExporter) Shutdown(ctx context.Context) error {
	<-ctx.Done()

	return ctx.Err()
}

type recordingListener struct {
	net.Listener

	accepts chan struct{}
}

func (l *recordingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		select {
		case l.accepts <- struct{}{}:
		default:
		}
	}

	return conn, err
}

type recordingTraceService struct {
	collectortracepb.UnimplementedTraceServiceServer

	requests chan metadata.MD
}

func (s *recordingTraceService) Export(ctx context.Context, _ *collectortracepb.ExportTraceServiceRequest) (*collectortracepb.ExportTraceServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)

	s.requests <- md.Copy()

	return &collectortracepb.ExportTraceServiceResponse{}, nil
}

func assertResourceAttr(t *testing.T, res *resource.Resource, key string, want any) {
	t.Helper()

	if res == nil {
		t.Fatalf("resource is nil, want %s=%v", key, want)
	}

	got, ok := res.Set().Value(attribute.Key(key))
	if !ok {
		t.Fatalf("resource missing %s", key)
	}

	switch want := want.(type) {
	case string:
		if got.AsString() != want {
			t.Fatalf("resource %s = %q, want %q", key, got.AsString(), want)
		}
	case int64:
		if got.AsInt64() != want {
			t.Fatalf("resource %s = %d, want %d", key, got.AsInt64(), want)
		}
	default:
		t.Fatalf("unsupported assertion type %T", want)
	}
}
