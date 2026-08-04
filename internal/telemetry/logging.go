package telemetry

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	collectorlogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

const (
	DaemonLogSchema = "graith.daemon_event.v1"

	LogProtocolGRPC         = ProtocolGRPC
	LogProtocolHTTPProtobuf = ProtocolHTTPProtobuf

	LogSeverityDebug = "DEBUG"
	LogSeverityInfo  = "INFO"
	LogSeverityWarn  = "WARN"
	LogSeverityError = "ERROR"
)

type LoggingOptions struct {
	Endpoint       string
	Protocol       string
	Insecure       bool
	Timeout        time.Duration
	Headers        map[string]string
	QueueSize      int
	BatchSize      int
	ExportInterval time.Duration
	Resource       ResourceOptions
	Logger         *slog.Logger
}

type LogEvent struct {
	Time       time.Time
	Severity   string
	Domain     string
	Name       string
	Result     string
	Attributes []attribute.KeyValue
}

type LogRecord struct {
	Time       time.Time
	ObservedAt time.Time
	Severity   string
	Domain     string
	Name       string
	Result     string
	Attributes []attribute.KeyValue
}

type LoggingRuntime struct {
	processor *boundedLogProcessor
	timeout   time.Duration
}

type logExporter interface {
	Export(ctx context.Context, records []LogRecord) error
	Shutdown(ctx context.Context) error
}

var newLogExporter = newOTLPLogExporter

func StartLogging(ctx context.Context, opts LoggingOptions) (*LoggingRuntime, error) {
	if ctx == nil {
		return nil, errors.New("telemetry logs context is required")
	}

	if opts.Endpoint == "" {
		return nil, errors.New("telemetry logs endpoint is required")
	}

	if opts.Protocol == "" {
		opts.Protocol = LogProtocolGRPC
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	opts.Timeout = timeout

	if opts.QueueSize <= 0 {
		opts.QueueSize = 2048
	}

	if opts.BatchSize <= 0 {
		opts.BatchSize = 512
	}

	if opts.BatchSize > opts.QueueSize {
		opts.BatchSize = opts.QueueSize
	}

	if opts.ExportInterval <= 0 {
		opts.ExportInterval = time.Second
	}

	setupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	exporter, err := newLogExporter(setupCtx, opts)
	if err != nil {
		return nil, fmt.Errorf("create OTLP log exporter: %w", err)
	}

	workerCtx := context.WithoutCancel(ctx)

	return &LoggingRuntime{
		processor: newBoundedLogProcessor(workerCtx, exporter, boundedLogProcessorConfig{
			QueueSize:      opts.QueueSize,
			BatchSize:      opts.BatchSize,
			ExportInterval: opts.ExportInterval,
			ExportTimeout:  timeout,
			Logger:         opts.Logger,
		}),
		timeout: timeout,
	}, nil
}

func (rt *LoggingRuntime) Emit(ctx context.Context, event LogEvent) error {
	if rt == nil || rt.processor == nil {
		return nil
	}

	record, err := NewLogRecord(event)
	if err != nil {
		return err
	}

	rt.processor.Emit(ctx, record)

	return nil
}

func (rt *LoggingRuntime) ForceFlush(ctx context.Context) error {
	if rt == nil || rt.processor == nil {
		return nil
	}

	if ctx == nil {
		return errors.New("telemetry logs flush context is required")
	}

	return rt.processor.ForceFlush(ctx)
}

func (rt *LoggingRuntime) Shutdown(ctx context.Context) error {
	if rt == nil {
		return nil
	}

	if ctx == nil {
		return errors.New("telemetry logs shutdown context is required")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	timeout := rt.timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if rt.processor != nil {
		return rt.processor.Shutdown(shutdownCtx)
	}

	return nil
}

func (rt *LoggingRuntime) Dropped() uint64 {
	if rt == nil || rt.processor == nil {
		return 0
	}

	return rt.processor.Dropped()
}

func PseudonymousRef(secret []byte, value string) string {
	if len(secret) == 0 || value == "" {
		return ""
	}

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	sum := mac.Sum(nil)

	return hex.EncodeToString(sum[:16])
}

func NewLogRecord(event LogEvent) (LogRecord, error) {
	domain, ok := allowedLogEventNames[event.Name]
	if !ok {
		return LogRecord{}, fmt.Errorf("telemetry log event %q is not allowlisted", event.Name)
	}

	if event.Domain == "" {
		event.Domain = domain
	}

	if event.Domain != domain {
		return LogRecord{}, fmt.Errorf("telemetry log event %q belongs to domain %q, not %q", event.Name, domain, event.Domain)
	}

	severity := normalizeSeverity(event.Severity)
	result := normalizeResult(event.Result)

	eventTime := event.Time
	if eventTime.IsZero() {
		eventTime = time.Now()
	}

	attrs := []attribute.KeyValue{
		attribute.String("schema", DaemonLogSchema),
		attribute.String("service.name", ServiceNameDefault),
		attribute.String("event.domain", event.Domain),
		attribute.String("event.name", event.Name),
		attribute.String("result", result),
	}

	safeAttrs, err := sanitizeLogAttributes(event.Attributes)
	if err != nil {
		return LogRecord{}, err
	}

	attrs = append(attrs, safeAttrs...)

	return LogRecord{
		Time:       eventTime.UTC(),
		ObservedAt: time.Now().UTC(),
		Severity:   severity,
		Domain:     event.Domain,
		Name:       event.Name,
		Result:     result,
		Attributes: attrs,
	}, nil
}

var allowedLogEventNames = map[string]string{
	"session.exited":         "session",
	"telemetry.logs_started": "telemetry",
}

var allowedLogResults = map[string]struct{}{
	"success":  {},
	"error":    {},
	"degraded": {},
	"skipped":  {},
	"denied":   {},
	"timeout":  {},
	"unknown":  {},
}

type logAttrKind int

const (
	logAttrString logAttrKind = iota
	logAttrEnum
	logAttrInt
	logAttrBool
	logAttrRef
)

var allowedLogAttributes = map[attribute.Key]logAttrKind{
	"agent_kind":            logAttrEnum,
	"duration_ms":           logAttrInt,
	"error.class":           logAttrEnum,
	"error.code":            logAttrEnum,
	"error.kind":            logAttrEnum,
	"error.retryable":       logAttrBool,
	"graith.commit":         logAttrString,
	"os.arch":               logAttrEnum,
	"os.type":               logAttrEnum,
	"process.exit_code":     logAttrInt,
	"process.pgid":          logAttrInt,
	"process.pid":           logAttrInt,
	"process.signal":        logAttrEnum,
	"profile_kind":          logAttrEnum,
	"queue_size":            logAttrInt,
	"batch_size":            logAttrInt,
	"service.version":       logAttrString,
	"session.driver_kind":   logAttrEnum,
	"session.exit_category": logAttrEnum,
	"session.ref":           logAttrRef,
	"session.sandboxed":     logAttrBool,
	"session.signal_source": logAttrEnum,
	"session.status":        logAttrEnum,
	"session.stop_reason":   logAttrEnum,
	"telemetry.protocol":    logAttrEnum,
}

var enumLogAttributeValues = map[attribute.Key]map[string]struct{}{
	"agent_kind":            enumSet("codex", "claude", "cursor", "opencode", "agy", "custom", "unknown"),
	"error.class":           enumSet("configuration", "export", "validation", "unknown"),
	"error.code":            enumSet("unknown"),
	"error.kind":            enumSet("context_canceled", "deadline_exceeded", "export_failed", "unknown"),
	"os.arch":               enumSet(runtime.GOARCH),
	"os.type":               enumSet(runtime.GOOS),
	"process.signal":        enumSet("SIGABRT", "SIGALRM", "SIGBUS", "SIGCHLD", "SIGCONT", "SIGFPE", "SIGHUP", "SIGILL", "SIGINT", "SIGKILL", "SIGPIPE", "SIGQUIT", "SIGSEGV", "SIGSTOP", "SIGTERM", "SIGTRAP", "SIGTSTP", "SIGTTIN", "SIGTTOU", "SIGURG", "SIGUSR1", "SIGUSR2", "SIGXCPU", "SIGXFSZ", "unknown"),
	"profile_kind":          enumSet("default", "custom"),
	"session.driver_kind":   enumSet("pty", "headless", "unknown"),
	"session.exit_category": enumSet("exit-clean", "exit-nonzero", "signal-after-graith-request", "signal-external-or-unknown", "unknown"),
	"session.signal_source": enumSet("none", "graith-requested", "external-or-unknown", "unknown"),
	"session.status":        enumSet("creating", "running", "stopped", "errored", "deleting", "unknown"),
	"session.stop_reason":   enumSet("crash", "idle", "user", "shutdown", "watchdog", "scenario-timeout", "delete", "convert", "unknown"),
	"telemetry.protocol":    enumSet(LogProtocolGRPC, LogProtocolHTTPProtobuf),
}

func sanitizeLogAttributes(attrs []attribute.KeyValue) ([]attribute.KeyValue, error) {
	if len(attrs) == 0 {
		return nil, nil
	}

	out := make([]attribute.KeyValue, 0, len(attrs))
	seen := map[attribute.Key]struct{}{}

	for _, attr := range attrs {
		kind, ok := allowedLogAttributes[attr.Key]
		if !ok {
			return nil, fmt.Errorf("telemetry log attribute %q is not allowlisted", attr.Key)
		}

		if _, exists := seen[attr.Key]; exists {
			return nil, fmt.Errorf("telemetry log attribute %q is duplicated", attr.Key)
		}

		seen[attr.Key] = struct{}{}

		switch kind {
		case logAttrString:
			if attr.Value.Type() != attribute.STRING {
				return nil, fmt.Errorf("telemetry log attribute %q has type %s, want STRING", attr.Key, attr.Value.Type())
			}

			value := attr.Value.AsString()

			sanitized, truncated := sanitizeLogString(value, 256)
			if sanitized == value && !truncated {
				out = append(out, attr)
				break
			}

			out = append(out, attribute.String(string(attr.Key), sanitized))
			if truncated {
				out = append(out,
					attribute.Bool(string(attr.Key)+".truncated", true),
					attribute.Int(string(attr.Key)+".original_bytes", len(value)),
				)
			}
		case logAttrEnum:
			if attr.Value.Type() != attribute.STRING {
				return nil, fmt.Errorf("telemetry log attribute %q has type %s, want STRING", attr.Key, attr.Value.Type())
			}

			if _, ok := enumLogAttributeValues[attr.Key][attr.Value.AsString()]; !ok {
				return nil, fmt.Errorf("telemetry log attribute %q has unlisted value %q", attr.Key, attr.Value.AsString())
			}

			out = append(out, attr)
		case logAttrInt:
			if attr.Value.Type() != attribute.INT64 {
				return nil, fmt.Errorf("telemetry log attribute %q has type %s, want INT64", attr.Key, attr.Value.Type())
			}

			out = append(out, attr)
		case logAttrBool:
			if attr.Value.Type() != attribute.BOOL {
				return nil, fmt.Errorf("telemetry log attribute %q has type %s, want BOOL", attr.Key, attr.Value.Type())
			}

			out = append(out, attr)
		case logAttrRef:
			if attr.Value.Type() != attribute.STRING {
				return nil, fmt.Errorf("telemetry log attribute %q has type %s, want STRING", attr.Key, attr.Value.Type())
			}

			value := attr.Value.AsString()
			if !isLowerHexRef(value) {
				return nil, fmt.Errorf("telemetry log attribute %q must be a 32-character lowercase hex pseudonymous ref", attr.Key)
			}

			out = append(out, attr)
		default:
			return nil, fmt.Errorf("telemetry log attribute %q has unknown allowlist kind", attr.Key)
		}
	}

	return out, nil
}

func isLowerHexRef(value string) bool {
	if len(value) != 32 {
		return false
	}

	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return false
		}
	}

	return true
}

func enumSet(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}

	return out
}

func normalizeSeverity(severity string) string {
	switch strings.ToUpper(strings.TrimSpace(severity)) {
	case LogSeverityDebug:
		return LogSeverityDebug
	case LogSeverityWarn:
		return LogSeverityWarn
	case LogSeverityError:
		return LogSeverityError
	default:
		return LogSeverityInfo
	}
}

func normalizeResult(result string) string {
	result = strings.ToLower(strings.TrimSpace(result))
	if _, ok := allowedLogResults[result]; ok {
		return result
	}

	return "unknown"
}

func sanitizeLogString(value string, maxBytes int) (string, bool) {
	value = strings.ToValidUTF8(value, "")
	if len(value) <= maxBytes {
		return value, false
	}

	for maxBytes > 0 && !utf8.ValidString(value[:maxBytes]) {
		maxBytes--
	}

	return value[:maxBytes], true
}

type boundedLogProcessorConfig struct {
	QueueSize      int
	BatchSize      int
	ExportInterval time.Duration
	ExportTimeout  time.Duration
	Logger         *slog.Logger
}

type boundedLogProcessor struct {
	exporter logExporter
	queue    chan LogRecord

	batchSize      int
	exportInterval time.Duration
	exportTimeout  time.Duration
	log            *slog.Logger

	flush    chan logProcessorRequest
	shutdown chan logProcessorRequest
	done     chan struct{}

	stopped         atomic.Bool
	dropped         atomic.Uint64
	reportedDropped atomic.Uint64
	exportFailures  atomic.Uint64
	shutdownErrMu   sync.Mutex
	shutdownErr     error
}

type logProcessorRequest struct {
	ctx  context.Context
	resp chan<- error
}

func newBoundedLogProcessor(ctx context.Context, exporter logExporter, cfg boundedLogProcessorConfig) *boundedLogProcessor {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 2048
	}

	if cfg.BatchSize <= 0 || cfg.BatchSize > cfg.QueueSize {
		cfg.BatchSize = cfg.QueueSize
	}

	if cfg.ExportInterval <= 0 {
		cfg.ExportInterval = time.Second
	}

	if cfg.ExportTimeout <= 0 {
		cfg.ExportTimeout = 10 * time.Second
	}

	p := &boundedLogProcessor{
		exporter:       exporter,
		queue:          make(chan LogRecord, cfg.QueueSize),
		batchSize:      cfg.BatchSize,
		exportInterval: cfg.ExportInterval,
		exportTimeout:  cfg.ExportTimeout,
		log:            cfg.Logger,
		flush:          make(chan logProcessorRequest),
		shutdown:       make(chan logProcessorRequest),
		done:           make(chan struct{}),
	}

	go p.run(ctx)

	return p
}

func (p *boundedLogProcessor) Emit(_ context.Context, record LogRecord) {
	if p == nil {
		return
	}

	if p.stopped.Load() {
		p.dropped.Add(1)

		return
	}

	select {
	case p.queue <- record:
		return
	default:
	}

	var dropped uint64

	select {
	case <-p.queue:
		dropped++
	default:
	}

	select {
	case p.queue <- record:
	default:
		dropped++
	}

	if dropped > 0 {
		p.dropped.Add(dropped)
	}
}

func (p *boundedLogProcessor) Dropped() uint64 {
	if p == nil {
		return 0
	}

	return p.dropped.Load()
}

func (p *boundedLogProcessor) ForceFlush(ctx context.Context) error {
	if p == nil {
		return nil
	}

	if p.stopped.Load() {
		select {
		case <-p.done:
			return p.shutdownResult()
		default:
			return nil
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	resp := make(chan error, 1)

	req := logProcessorRequest{ctx: ctx, resp: resp}

	select {
	case p.flush <- req:
	case <-p.done:
		return p.shutdownResult()
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-resp:
		return err
	case <-p.done:
		return p.shutdownResult()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *boundedLogProcessor) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if !p.stopped.CompareAndSwap(false, true) {
		select {
		case <-p.done:
			return p.shutdownResult()
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	resp := make(chan error, 1)

	req := logProcessorRequest{ctx: ctx, resp: resp}

	select {
	case p.shutdown <- req:
	case <-p.done:
		return p.shutdownResult()
	case <-ctx.Done():
		p.stopped.Store(false)

		return ctx.Err()
	}

	select {
	case err := <-resp:
		return err
	case <-p.done:
		return p.shutdownResult()
	case <-ctx.Done():
		return ctx.Err()
	}
}

//nolint:contextcheck // Flush and shutdown requests carry caller contexts over channels.
func (p *boundedLogProcessor) run(ctx context.Context) {
	timer := time.NewTimer(p.exportInterval)
	defer timer.Stop()

	batch := make([]LogRecord, 0, p.batchSize)

	for {
		select {
		case req := <-p.flush:
			req.resp <- p.flushBatch(req.ctx, &batch)

			resetLogTimer(timer, p.exportInterval)
		case req := <-p.shutdown:
			err := p.flushBatch(req.ctx, &batch)
			err = errors.Join(err, p.exporter.Shutdown(req.ctx))

			p.reportDrops()
			p.setShutdownErr(err)

			req.resp <- err

			close(p.done)

			return
		case record := <-p.queue:
			batch = append(batch, record)
			if len(batch) >= p.batchSize {
				p.exportWithTimeout(ctx, &batch)
				resetLogTimer(timer, p.exportInterval)
			}
		case <-timer.C:
			p.exportWithTimeout(ctx, &batch)
			resetLogTimer(timer, p.exportInterval)
		}
	}
}

func (p *boundedLogProcessor) flushBatch(ctx context.Context, batch *[]LogRecord) error {
	err := p.drainQueue(ctx, batch)
	err = errors.Join(err, p.exportBatch(ctx, batch))

	p.reportDrops()

	return err
}

func (p *boundedLogProcessor) exportWithTimeout(ctx context.Context, batch *[]LogRecord) {
	ctx, cancel := context.WithTimeout(ctx, p.exportTimeout)
	defer cancel()

	_ = p.exportBatch(ctx, batch)

	p.reportDrops()
}

func (p *boundedLogProcessor) exportBatch(ctx context.Context, batch *[]LogRecord) error {
	if len(*batch) == 0 {
		return nil
	}

	records := slices.Clone(*batch)
	clear(*batch)
	*batch = (*batch)[:0]

	if err := p.exporter.Export(ctx, records); err != nil {
		p.dropped.Add(droppedLogRecordsForExportError(err, len(records)))
		p.reportExportFailure(err)

		return err
	}

	return nil
}

func (p *boundedLogProcessor) setShutdownErr(err error) {
	p.shutdownErrMu.Lock()
	defer p.shutdownErrMu.Unlock()

	p.shutdownErr = err
}

func (p *boundedLogProcessor) shutdownResult() error {
	p.shutdownErrMu.Lock()
	defer p.shutdownErrMu.Unlock()

	return p.shutdownErr
}

func droppedLogRecordsForExportError(err error, batchLen int) uint64 {
	var partial partialLogExportError
	if errors.As(err, &partial) {
		rejected := partial.rejected
		if rejected < 0 {
			rejected = 0
		}

		if rejected > int64(batchLen) {
			rejected = int64(batchLen)
		}

		return uint64(rejected)
	}

	return uint64(batchLen) //nolint:gosec // batchLen comes from len(records) and is non-negative.
}

func (p *boundedLogProcessor) drainQueue(ctx context.Context, batch *[]LogRecord) error {
	var err error

	for {
		select {
		case record := <-p.queue:
			*batch = append(*batch, record)
			if len(*batch) >= p.batchSize {
				err = errors.Join(err, p.exportBatch(ctx, batch))
			}
		default:
			return err
		}
	}
}

func (p *boundedLogProcessor) reportExportFailure(err error) {
	if p.log == nil || err == nil {
		return
	}

	total := p.exportFailures.Add(1)
	if total != 1 && total&(total-1) != 0 {
		return
	}

	p.log.Warn("telemetry log export failed", "err", err, "total_failures", total)
}

func (p *boundedLogProcessor) reportDrops() {
	for {
		total := p.dropped.Load()

		reported := p.reportedDropped.Load()

		if total == reported {
			return
		}

		if p.reportedDropped.CompareAndSwap(reported, total) {
			if p.log != nil {
				p.log.Warn("telemetry log export events dropped",
					"dropped", total-reported,
					"total_dropped", total,
					"queue_size", cap(p.queue))
			}

			return
		}
	}
}

func resetLogTimer(timer *time.Timer, interval time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}

	timer.Reset(interval)
}

type otlpLogExporter struct {
	protocol string
	endpoint string
	headers  map[string]string

	resourceAttrs []*commonpb.KeyValue
	httpClient    *http.Client
	grpcConn      *grpc.ClientConn
	grpcClient    collectorlogpb.LogsServiceClient
}

func newOTLPLogExporter(_ context.Context, opts LoggingOptions) (logExporter, error) {
	exp := &otlpLogExporter{
		protocol:      opts.Protocol,
		endpoint:      opts.Endpoint,
		headers:       cloneHeaders(opts.Headers),
		resourceAttrs: logResourceAttributes(opts.Resource),
	}

	switch opts.Protocol {
	case LogProtocolGRPC:
		transportCredentials := credentials.NewTLS(nil)
		if opts.Insecure {
			transportCredentials = insecure.NewCredentials()
		}

		conn, err := grpc.NewClient(
			opts.Endpoint,
			grpc.WithTransportCredentials(transportCredentials),
			grpc.WithUserAgent("graith-otlp-logs"),
		)
		if err != nil {
			return nil, fmt.Errorf("create OTLP logs gRPC client: %w", err)
		}

		exp.grpcConn = conn
		exp.grpcClient = collectorlogpb.NewLogsServiceClient(conn)
	case LogProtocolHTTPProtobuf:
		exp.httpClient = newTraceHTTPClient(opts.Timeout)
	default:
		return nil, fmt.Errorf("unsupported logs protocol %q", opts.Protocol)
	}

	return exp, nil
}

func (e *otlpLogExporter) Export(ctx context.Context, records []LogRecord) error {
	if len(records) == 0 {
		return nil
	}

	req := e.exportRequest(records)
	switch e.protocol {
	case LogProtocolGRPC:
		if len(e.headers) > 0 {
			ctx = metadata.NewOutgoingContext(ctx, metadata.New(e.headers))
		}

		resp, err := e.grpcClient.Export(ctx, req)
		if err != nil {
			return err
		}

		return otlpLogPartialSuccessError(resp.GetPartialSuccess())
	case LogProtocolHTTPProtobuf:
		data, err := proto.Marshal(req)
		if err != nil {
			return fmt.Errorf("marshal OTLP logs request: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("create OTLP logs HTTP request: %w", err)
		}

		for name, value := range e.headers {
			httpReq.Header.Set(name, value)
		}

		httpReq.Header.Set("content-type", "application/x-protobuf")
		httpReq.Header.Set("user-agent", "graith-otlp-logs")

		resp, err := e.httpClient.Do(httpReq)
		if err != nil {
			return err
		}

		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read OTLP logs HTTP response: %w", err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("OTLP logs HTTP status %s", resp.Status)
		}

		if len(body) == 0 {
			return nil
		}

		var exportResp collectorlogpb.ExportLogsServiceResponse
		if err := proto.Unmarshal(body, &exportResp); err != nil {
			return fmt.Errorf("decode OTLP logs HTTP response: %w", err)
		}

		return otlpLogPartialSuccessError(exportResp.GetPartialSuccess())
	default:
		return fmt.Errorf("unsupported logs protocol %q", e.protocol)
	}
}

type partialLogExportError struct {
	rejected int64
	message  string
}

func (e partialLogExportError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("OTLP logs partial success rejected %d log record(s)", e.rejected)
	}

	return fmt.Sprintf("OTLP logs partial success rejected %d log record(s): %s", e.rejected, e.message)
}

func otlpLogPartialSuccessError(partial *collectorlogpb.ExportLogsPartialSuccess) error {
	if partial == nil || partial.GetRejectedLogRecords() <= 0 {
		return nil
	}

	return partialLogExportError{
		rejected: partial.GetRejectedLogRecords(),
		message:  partial.GetErrorMessage(),
	}
}

func (e *otlpLogExporter) Shutdown(_ context.Context) error {
	if e == nil || e.grpcConn == nil {
		return nil
	}

	return e.grpcConn.Close()
}

func (e *otlpLogExporter) exportRequest(records []LogRecord) *collectorlogpb.ExportLogsServiceRequest {
	logRecords := make([]*logspb.LogRecord, 0, len(records))
	for _, record := range records {
		logRecords = append(logRecords, otlpLogRecord(record))
	}

	return &collectorlogpb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{
			{
				Resource: &resourcepb.Resource{
					Attributes: e.resourceAttrs,
				},
				ScopeLogs: []*logspb.ScopeLogs{
					{
						Scope: &commonpb.InstrumentationScope{
							Name: "github.com/d0ugal/graith/internal/daemon",
						},
						LogRecords: logRecords,
					},
				},
			},
		},
	}
}

func otlpLogRecord(record LogRecord) *logspb.LogRecord {
	body := &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: record.Name}}

	return &logspb.LogRecord{
		TimeUnixNano:         uint64(record.Time.UnixNano()),
		ObservedTimeUnixNano: uint64(record.ObservedAt.UnixNano()),
		SeverityNumber:       otlpSeverityNumber(record.Severity),
		SeverityText:         record.Severity,
		Body:                 body,
		Attributes:           attributeKeyValues(record.Attributes),
		EventName:            record.Name,
	}
}

func logResourceAttributes(opts ResourceOptions) []*commonpb.KeyValue {
	// Keep this separate from newTracingResource: log resources are part of the
	// safe export schema and must not include daemon instance IDs, raw profile
	// names, executable names, or other host/session identity.
	serviceVersion := opts.ServiceVersion
	if serviceVersion == "" {
		serviceVersion = "dev"
	}

	commit := opts.Commit
	if commit == "" {
		commit = "unknown"
	}

	profileKind := "default"
	if opts.Profile != "" {
		profileKind = "custom"
	}

	attrs := []attribute.KeyValue{
		attribute.String("service.name", ServiceNameDefault),
		attribute.String("service.version", serviceVersion),
		attribute.String("graith.commit", commit),
		attribute.String("graith.process.kind", "daemon"),
		attribute.String("profile_kind", profileKind),
		attribute.String("os.type", runtime.GOOS),
		attribute.String("os.arch", runtime.GOARCH),
	}
	if opts.ProcessPID > 0 {
		attrs = append(attrs, attribute.Int("process.pid", opts.ProcessPID))
	}

	return attributeKeyValues(attrs)
}

func attributeKeyValues(attrs []attribute.KeyValue) []*commonpb.KeyValue {
	out := make([]*commonpb.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		value, ok := anyValue(attr.Value)
		if !ok {
			continue
		}

		out = append(out, &commonpb.KeyValue{
			Key:   string(attr.Key),
			Value: value,
		})
	}

	return out
}

func anyValue(value attribute.Value) (*commonpb.AnyValue, bool) {
	switch value.Type() {
	case attribute.STRING:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value.AsString()}}, true
	case attribute.BOOL:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: value.AsBool()}}, true
	case attribute.INT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value.AsInt64()}}, true
	case attribute.FLOAT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: value.AsFloat64()}}, true
	default:
		return nil, false
	}
}

func otlpSeverityNumber(severity string) logspb.SeverityNumber {
	switch severity {
	case LogSeverityDebug:
		return logspb.SeverityNumber_SEVERITY_NUMBER_DEBUG
	case LogSeverityWarn:
		return logspb.SeverityNumber_SEVERITY_NUMBER_WARN
	case LogSeverityError:
		return logspb.SeverityNumber_SEVERITY_NUMBER_ERROR
	default:
		return logspb.SeverityNumber_SEVERITY_NUMBER_INFO
	}
}
