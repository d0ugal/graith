package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.28.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	ServiceNameDefault = "graith-daemon"

	ProtocolGRPC         = "grpc"
	ProtocolHTTPProtobuf = "http/protobuf"
)

// TracingOptions are the daemon-supplied settings for optional OTLP trace
// export. Callers pass explicit endpoint/protocol values from Graith config;
// this package overrides OpenTelemetry environment-derived exporter settings
// with Graith's explicit config before starting an exporter.
type TracingOptions struct {
	Endpoint string
	Protocol string
	Insecure bool
	Timeout  time.Duration
	Headers  map[string]string
	Resource ResourceOptions
	Logger   *slog.Logger
}

type ResourceOptions struct {
	ServiceName           string
	ServiceVersion        string
	Commit                string
	DaemonInstanceID      string
	Profile               string
	ProcessKind           string
	ProcessPID            int
	ProcessExecutableName string
}

type TracingRuntime struct {
	provider              *sdktrace.TracerProvider
	timeout               time.Duration
	errorHandlerInstalled bool
	stopOnce              sync.Once
	stopErr               error
}

var (
	newTraceExporter = newOTLPTraceExporter
	newTraceProvider = newSDKTraceProvider
)

// Tracer returns a tracer from the current global provider. Callers should
// request tracers when needed rather than caching them across daemon restarts.
func Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return otel.Tracer(name, opts...)
}

func StartTracing(ctx context.Context, opts TracingOptions) (*TracingRuntime, error) {
	if ctx == nil {
		return nil, errors.New("telemetry tracing context is required")
	}

	if opts.Endpoint == "" {
		return nil, errors.New("telemetry tracing endpoint is required")
	}

	if opts.Protocol == "" {
		opts.Protocol = ProtocolGRPC
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	opts.Timeout = timeout

	setupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	exporter, err := newTraceExporter(setupCtx, opts)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}

	logger := opts.Logger

	errorHandlerInstalled := logger != nil
	if errorHandlerInstalled {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			if err != nil {
				logger.Warn("telemetry tracing exporter error", "err", err)
			}
		}))
	}

	provider := newTraceProvider(exporter, newTracingResource(opts.Resource), timeout)
	otel.SetTracerProvider(provider)

	return &TracingRuntime{
		provider:              provider,
		timeout:               timeout,
		errorHandlerInstalled: errorHandlerInstalled,
	}, nil
}

func (rt *TracingRuntime) Shutdown(ctx context.Context) error {
	if rt == nil {
		return nil
	}

	if ctx == nil {
		return errors.New("telemetry tracing shutdown context is required")
	}

	rt.stopOnce.Do(func() {
		rt.stopErr = rt.shutdown(ctx)
	})

	return rt.stopErr
}

func (rt *TracingRuntime) shutdown(ctx context.Context) error {
	timeout := rt.timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	var err error

	if rt.provider != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		err = errors.Join(err, rt.provider.ForceFlush(shutdownCtx))
		err = errors.Join(err, rt.provider.Shutdown(shutdownCtx))
	}

	otel.SetTracerProvider(noop.NewTracerProvider())

	if rt.errorHandlerInstalled {
		resetTraceErrorHandler()
	}

	return err
}

func resetTraceErrorHandler() {
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {}))
}

func newOTLPTraceExporter(ctx context.Context, opts TracingOptions) (sdktrace.SpanExporter, error) {
	switch opts.Protocol {
	case ProtocolGRPC:
		transportCredentials := credentials.NewTLS(nil)
		if opts.Insecure {
			transportCredentials = insecure.NewCredentials()
		}

		conn, err := grpc.NewClient(
			opts.Endpoint,
			grpc.WithTransportCredentials(transportCredentials),
			grpc.WithUserAgent("graith-otlp-traces"),
		)
		if err != nil {
			return nil, fmt.Errorf("create OTLP gRPC client: %w", err)
		}

		exporterOpts := []otlptracegrpc.Option{
			otlptracegrpc.WithGRPCConn(conn),
			otlptracegrpc.WithHeaders(cloneHeaders(opts.Headers)),
			otlptracegrpc.WithTimeout(opts.Timeout),
		}

		exporter, err := otlptracegrpc.New(ctx, exporterOpts...)
		if err != nil {
			return nil, errors.Join(err, conn.Close())
		}

		return &managedGRPCTraceExporter{
			exporter: exporter,
			conn:     conn,
		}, nil
	case ProtocolHTTPProtobuf:
		exporterOpts := []otlptracehttp.Option{
			otlptracehttp.WithEndpointURL(opts.Endpoint),
			otlptracehttp.WithHeaders(cloneHeaders(opts.Headers)),
			otlptracehttp.WithCompression(otlptracehttp.NoCompression),
			otlptracehttp.WithHTTPClient(newTraceHTTPClient(opts.Timeout)),
			otlptracehttp.WithTimeout(opts.Timeout),
		}

		return otlptracehttp.New(ctx, exporterOpts...)
	default:
		return nil, fmt.Errorf("unsupported tracing protocol %q", opts.Protocol)
	}
}

type managedGRPCTraceExporter struct {
	exporter sdktrace.SpanExporter
	conn     *grpc.ClientConn
}

func (e *managedGRPCTraceExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	return e.exporter.ExportSpans(ctx, spans)
}

func (e *managedGRPCTraceExporter) Shutdown(ctx context.Context) error {
	var err error

	if e.exporter != nil {
		err = e.exporter.Shutdown(ctx)
	}

	if e.conn != nil {
		err = errors.Join(err, e.conn.Close())
	}

	return err
}

func newTraceHTTPClient(timeout time.Duration) *http.Client {
	tlsHandshakeTimeout := 10 * time.Second
	if timeout > 0 && timeout < tlsHandshakeTimeout {
		tlsHandshakeTimeout = timeout
	}

	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   timeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        100,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: tlsHandshakeTimeout,
		},
		Timeout: timeout,
	}
}

func newSDKTraceProvider(exporter sdktrace.SpanExporter, res *resource.Resource, timeout time.Duration) *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithExportTimeout(timeout)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
}

func newTracingResource(opts ResourceOptions) *resource.Resource {
	serviceName := opts.ServiceName
	if serviceName == "" {
		serviceName = ServiceNameDefault
	}

	processKind := opts.ProcessKind
	if processKind == "" {
		processKind = "daemon"
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceName(serviceName),
		attribute.String("graith.process.kind", processKind),
	}
	if opts.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(opts.ServiceVersion))
	}

	if opts.Commit != "" {
		attrs = append(attrs, attribute.String("graith.commit", opts.Commit))
	}

	if opts.DaemonInstanceID != "" {
		attrs = append(attrs,
			semconv.ServiceInstanceID(opts.DaemonInstanceID),
			attribute.String("graith.daemon.instance_id", opts.DaemonInstanceID),
		)
	}

	if opts.Profile != "" {
		attrs = append(attrs, attribute.String("graith.profile", opts.Profile))
	}

	if opts.ProcessPID > 0 {
		attrs = append(attrs, semconv.ProcessPID(opts.ProcessPID))
	}

	if opts.ProcessExecutableName != "" {
		attrs = append(attrs, semconv.ProcessExecutableName(opts.ProcessExecutableName))
	}

	return resource.NewWithAttributes(semconv.SchemaURL, attrs...)
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}

	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = v
	}

	return out
}
