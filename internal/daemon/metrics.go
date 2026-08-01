package daemon

import (
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/version"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	metricLabelUnknown  = "unknown"
	metricResultError   = "error"
	metricResultSuccess = "success"

	metricOperationAttach             = "attach"
	metricOperationCreate             = "create"
	metricOperationFork               = "fork"
	metricOperationOrchestratorCreate = "orchestrator_create"
	metricOperationResume             = "resume"
	metricOperationType               = "type"
	metricOperationTypeNoNewline      = "type_no_newline"

	metricSenderDevice  = "device"
	metricSenderSession = "session"
	metricSenderSystem  = "system"

	metricSnapshotDelta = "delta"
	metricSnapshotFull  = "full"

	metricAttachOutputCoalesced = "coalesced"
	metricAttachOutputRaw       = "raw"

	metricStreamInbox  = "inbox"
	metricStreamSystem = "system"
	metricStreamTopic  = "topic"
)

var (
	metricDriverKinds     = []string{DriverPTY, DriverHeadless, metricLabelUnknown}
	metricResults         = []string{metricResultSuccess, metricResultError}
	metricSessionStatuses = []string{
		string(StatusCreating),
		string(StatusRunning),
		string(StatusStopped),
		string(StatusErrored),
		string(StatusDeleting),
		metricLabelUnknown,
	}
)

type daemonMetrics struct {
	daemonInfo                 *prometheus.GaugeVec
	daemonUptime               prometheus.Collector
	sessionCounts              *sessionCountCollector
	attachedClients            prometheus.Collector
	sessionLaunchDuration      *prometheus.HistogramVec
	sessionLifecycleTransition *prometheus.CounterVec
	sessionInputEvents         *prometheus.CounterVec
	sessionInputBytes          *prometheus.CounterVec
	sessionInputDuration       *prometheus.HistogramVec
	sessionInputReadback       *prometheus.HistogramVec
	ptyOutputReadDuration      *prometheus.HistogramVec
	ptyScreenUpdateDuration    *prometheus.HistogramVec
	ptyAttachFanoutDuration    *prometheus.HistogramVec
	attachOutputQueueDelay     *prometheus.HistogramVec
	attachOutputWriteDuration  *prometheus.HistogramVec
	screenSnapshotRequests     *prometheus.CounterVec
	screenSnapshotDuration     *prometheus.HistogramVec
	messagesPublished          *prometheus.CounterVec
}

func newDaemonMetrics(sm *SessionManager) *daemonMetrics {
	return &daemonMetrics{
		daemonInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "graith",
			Subsystem: "daemon",
			Name:      "info",
			Help:      "Build information for the running Graith daemon.",
		}, []string{"version", "commit"}),
		daemonUptime: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "graith",
			Subsystem: "daemon",
			Name:      "uptime_seconds",
			Help:      "Seconds since the Graith daemon process started.",
		}, func() float64 {
			if sm.startedAt.IsZero() {
				return 0
			}

			return time.Since(sm.startedAt).Seconds()
		}),
		sessionCounts: newSessionCountCollector(sm),
		attachedClients: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "graith",
			Subsystem: "daemon",
			Name:      "attached_clients",
			Help:      "Number of clients currently attached to daemon sessions.",
		}, func() float64 {
			sm.mu.RLock()
			defer sm.mu.RUnlock()

			return float64(len(sm.attachedClients))
		}),
		sessionLaunchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "graith",
			Subsystem: "session",
			Name:      "launch_duration_seconds",
			Help:      "Duration of Graith session process launch attempts.",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"operation", "driver_kind", "result"}),
		sessionLifecycleTransition: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "graith",
			Subsystem: "session",
			Name:      "lifecycle_transitions_total",
			Help:      "Total published Graith session status-change events.",
		}, []string{"from", "to"}),
		sessionInputEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "graith",
			Subsystem: "session",
			Name:      "input_events_total",
			Help:      "Total Graith session input write attempts.",
		}, []string{"operation", "result"}),
		sessionInputBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "graith",
			Subsystem: "session",
			Name:      "input_bytes_total",
			Help:      "Total bytes submitted through Graith session input write attempts.",
		}, []string{"operation", "result"}),
		sessionInputDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "graith",
			Subsystem: "session",
			Name:      "input_duration_seconds",
			Help:      "Duration of Graith session input write attempts.",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}, []string{"operation", "result"}),
		sessionInputReadback: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "graith",
			Subsystem: "session",
			Name:      "input_readback_latency_seconds",
			Help:      "Duration from a successful session input write attempt to the next eligible PTY output read.",
			Buckets:   []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}, []string{"operation"}),
		ptyOutputReadDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "graith",
			Subsystem: "pty",
			Name:      "output_read_duration_seconds",
			Help:      "Duration from PTY readiness notification to PTY output read completion.",
			Buckets:   []float64{0.0001, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1},
		}, []string{"result"}),
		ptyScreenUpdateDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "graith",
			Subsystem: "pty",
			Name:      "screen_update_duration_seconds",
			Help:      "Duration to enter the screen update section, append PTY output, and update the daemon terminal screen model.",
			Buckets:   []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25},
		}, []string{"result"}),
		ptyAttachFanoutDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "graith",
			Subsystem: "pty",
			Name:      "attach_fanout_duration_seconds",
			Help:      "Duration to fan PTY output chunks out to attached daemon writers.",
			Buckets:   []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25},
		}, []string{"result"}),
		attachOutputQueueDelay: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "graith",
			Subsystem: "attach",
			Name:      "output_queue_delay_seconds",
			Help:      "Duration attached output waits in the daemon writer queue before flush begins.",
			Buckets:   []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}, []string{"mode"}),
		attachOutputWriteDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "graith",
			Subsystem: "attach",
			Name:      "output_write_duration_seconds",
			Help:      "Duration to write an attached output frame to the client connection.",
			Buckets:   []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}, []string{"mode", "result"}),
		screenSnapshotRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "graith",
			Subsystem: "screen",
			Name:      "snapshot_requests_total",
			Help:      "Total screen snapshot requests served by the daemon.",
		}, []string{"kind"}),
		screenSnapshotDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "graith",
			Subsystem: "screen",
			Name:      "snapshot_duration_seconds",
			Help:      "Duration of screen snapshot requests served by the daemon.",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}, []string{"kind"}),
		messagesPublished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "graith",
			Subsystem: "messages",
			Name:      "published_total",
			Help:      "Total messages published through the daemon message store.",
		}, []string{"stream_kind", "sender_kind"}),
	}
}

func (m *daemonMetrics) register(registry *prometheus.Registry) error {
	m.initBoundedLabels()

	for _, collector := range []prometheus.Collector{
		m.daemonInfo,
		m.daemonUptime,
		m.sessionCounts,
		m.attachedClients,
		m.sessionLaunchDuration,
		m.sessionLifecycleTransition,
		m.sessionInputEvents,
		m.sessionInputBytes,
		m.sessionInputDuration,
		m.sessionInputReadback,
		m.ptyOutputReadDuration,
		m.ptyScreenUpdateDuration,
		m.ptyAttachFanoutDuration,
		m.attachOutputQueueDelay,
		m.attachOutputWriteDuration,
		m.screenSnapshotRequests,
		m.screenSnapshotDuration,
		m.messagesPublished,
	} {
		if err := registry.Register(collector); err != nil {
			return err
		}
	}

	return nil
}

func (m *daemonMetrics) initBoundedLabels() {
	m.daemonInfo.WithLabelValues(metricBuildLabel(version.Version), metricBuildLabel(version.CommitSHA)).Set(1)

	for _, driverKind := range metricDriverKinds {
		for _, result := range metricResults {
			for _, operation := range []string{
				metricOperationCreate,
				metricOperationFork,
				metricOperationOrchestratorCreate,
				metricOperationResume,
				metricLabelUnknown,
			} {
				m.sessionLaunchDuration.WithLabelValues(operation, driverKind, result)
			}
		}
	}

	for _, from := range metricSessionStatuses {
		for _, to := range metricSessionStatuses {
			if from == to {
				continue
			}

			m.sessionLifecycleTransition.WithLabelValues(from, to)
		}
	}

	for _, result := range metricResults {
		for _, operation := range []string{
			metricOperationAttach,
			metricOperationType,
			metricOperationTypeNoNewline,
			metricLabelUnknown,
		} {
			m.sessionInputEvents.WithLabelValues(operation, result)
			m.sessionInputBytes.WithLabelValues(operation, result)
			m.sessionInputDuration.WithLabelValues(operation, result)
		}
	}

	for _, operation := range []string{
		metricOperationAttach,
		metricOperationType,
		metricOperationTypeNoNewline,
		metricLabelUnknown,
	} {
		m.sessionInputReadback.WithLabelValues(operation)
	}

	for _, result := range metricResults {
		m.ptyOutputReadDuration.WithLabelValues(result)
		m.ptyScreenUpdateDuration.WithLabelValues(result)
		m.ptyAttachFanoutDuration.WithLabelValues(result)

		for _, mode := range []string{metricAttachOutputRaw, metricAttachOutputCoalesced, metricLabelUnknown} {
			m.attachOutputWriteDuration.WithLabelValues(mode, result)
		}
	}

	for _, mode := range []string{metricAttachOutputRaw, metricAttachOutputCoalesced, metricLabelUnknown} {
		m.attachOutputQueueDelay.WithLabelValues(mode)
	}

	for _, kind := range []string{metricSnapshotFull, metricSnapshotDelta, metricLabelUnknown} {
		m.screenSnapshotRequests.WithLabelValues(kind)
		m.screenSnapshotDuration.WithLabelValues(kind)
	}

	for _, streamKind := range []string{metricStreamTopic, metricStreamInbox, metricStreamSystem, metricLabelUnknown} {
		for _, senderKind := range []string{metricSenderSession, metricSenderDevice, metricSenderSystem, metricLabelUnknown} {
			m.messagesPublished.WithLabelValues(streamKind, senderKind)
		}
	}
}

func (sm *SessionManager) observeSessionLaunch(operation, driverKind string, duration time.Duration, err error) {
	metrics := sm.metrics.Load()
	if metrics == nil {
		return
	}

	metrics.sessionLaunchDuration.WithLabelValues(
		metricLaunchOperation(operation),
		metricDriverKind(driverKind),
		metricResult(err),
	).Observe(duration.Seconds())
}

func (sm *SessionManager) observeSessionLifecycleTransition(from, to string) {
	metrics := sm.metrics.Load()
	if metrics == nil {
		return
	}

	fromStatus, toStatus := metricSessionStatus(SessionStatus(from)), metricSessionStatus(SessionStatus(to))
	if fromStatus == toStatus {
		return
	}

	metrics.sessionLifecycleTransition.WithLabelValues(fromStatus, toStatus).Inc()
}

func (sm *SessionManager) observeSessionInput(operation string, bytes int, duration time.Duration, err error) {
	metrics := sm.metrics.Load()
	if metrics == nil {
		return
	}

	if bytes < 0 {
		bytes = 0
	}

	operation = metricInputOperation(operation)
	result := metricResult(err)

	metrics.sessionInputEvents.WithLabelValues(operation, result).Inc()
	metrics.sessionInputBytes.WithLabelValues(operation, result).Add(float64(bytes))
	metrics.sessionInputDuration.WithLabelValues(operation, result).Observe(duration.Seconds())
}

func (sm *SessionManager) observeSessionInputReadback(operation string, duration time.Duration) {
	metrics := sm.metrics.Load()
	if metrics == nil {
		return
	}

	if duration < 0 {
		duration = 0
	}

	metrics.sessionInputReadback.WithLabelValues(metricInputOperation(operation)).Observe(duration.Seconds())
}

func (sm *SessionManager) observePTYOutputRead(duration time.Duration, err error) {
	metrics := sm.metrics.Load()
	if metrics == nil {
		return
	}

	if duration < 0 {
		duration = 0
	}

	metrics.ptyOutputReadDuration.WithLabelValues(metricResult(err)).Observe(duration.Seconds())
}

func (sm *SessionManager) observePTYScreenUpdate(duration time.Duration, err error) {
	metrics := sm.metrics.Load()
	if metrics == nil {
		return
	}

	if duration < 0 {
		duration = 0
	}

	metrics.ptyScreenUpdateDuration.WithLabelValues(metricResult(err)).Observe(duration.Seconds())
}

func (sm *SessionManager) observePTYAttachFanout(duration time.Duration, err error) {
	metrics := sm.metrics.Load()
	if metrics == nil {
		return
	}

	if duration < 0 {
		duration = 0
	}

	metrics.ptyAttachFanoutDuration.WithLabelValues(metricResult(err)).Observe(duration.Seconds())
}

func (sm *SessionManager) observeAttachOutputQueueDelay(mode attachOutputMode, duration time.Duration) {
	metrics := sm.metrics.Load()
	if metrics == nil {
		return
	}

	if duration < 0 {
		duration = 0
	}

	metrics.attachOutputQueueDelay.WithLabelValues(metricAttachOutputMode(mode)).Observe(duration.Seconds())
}

func (sm *SessionManager) observeAttachOutputWrite(mode attachOutputMode, duration time.Duration, err error) {
	metrics := sm.metrics.Load()
	if metrics == nil {
		return
	}

	if duration < 0 {
		duration = 0
	}

	metrics.attachOutputWriteDuration.WithLabelValues(metricAttachOutputMode(mode), metricResult(err)).Observe(duration.Seconds())
}

func (sm *SessionManager) observeScreenSnapshot(kind string, duration time.Duration) {
	metrics := sm.metrics.Load()
	if metrics == nil {
		return
	}

	kind = metricSnapshotKind(kind)
	metrics.screenSnapshotRequests.WithLabelValues(kind).Inc()
	metrics.screenSnapshotDuration.WithLabelValues(kind).Observe(duration.Seconds())
}

func (sm *SessionManager) observeMessagePublished(msg Message) {
	metrics := sm.metrics.Load()
	if metrics == nil {
		return
	}

	metrics.messagesPublished.WithLabelValues(metricStreamKind(msg.Stream), metricSenderKind(msg.SenderID)).Inc()
}

type sessionCountCollector struct {
	sm   *SessionManager
	desc *prometheus.Desc
}

func newSessionCountCollector(sm *SessionManager) *sessionCountCollector {
	return &sessionCountCollector{
		sm: sm,
		desc: prometheus.NewDesc(
			prometheus.BuildFQName("graith", "", "sessions"),
			"Number of non-deleted Graith sessions by status and driver kind.",
			[]string{"status", "driver_kind"},
			nil,
		),
	}
}

func (c *sessionCountCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (c *sessionCountCollector) Collect(ch chan<- prometheus.Metric) {
	counts := make(map[[2]string]float64, len(metricSessionStatuses)*len(metricDriverKinds))
	for _, status := range metricSessionStatuses {
		for _, driverKind := range metricDriverKinds {
			counts[[2]string{status, driverKind}] = 0
		}
	}

	c.sm.mu.RLock()

	for _, session := range c.sm.state.Sessions {
		if session == nil || session.IsSoftDeleted() {
			continue
		}

		key := [2]string{
			metricSessionStatus(session.Status),
			metricDriverKind(session.DriverKind),
		}
		counts[key]++
	}

	c.sm.mu.RUnlock()

	for _, status := range metricSessionStatuses {
		for _, driverKind := range metricDriverKinds {
			ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, counts[[2]string{status, driverKind}], status, driverKind)
		}
	}
}

func metricBuildLabel(value string) string {
	if value == "" {
		return metricLabelUnknown
	}

	return value
}

func metricDriverKind(driverKind string) string {
	switch driverKind {
	case "", DriverPTY:
		return DriverPTY
	case DriverHeadless:
		return DriverHeadless
	default:
		return metricLabelUnknown
	}
}

func metricInputOperation(operation string) string {
	switch operation {
	case metricOperationAttach, metricOperationType, metricOperationTypeNoNewline:
		return operation
	default:
		return metricLabelUnknown
	}
}

func metricAttachOutputMode(mode attachOutputMode) string {
	switch mode {
	case attachOutputRaw:
		return metricAttachOutputRaw
	case attachOutputCoalesced:
		return metricAttachOutputCoalesced
	default:
		return metricLabelUnknown
	}
}

func metricLaunchOperation(operation string) string {
	switch operation {
	case metricOperationCreate, metricOperationFork, metricOperationOrchestratorCreate, metricOperationResume:
		return operation
	default:
		return metricLabelUnknown
	}
}

func metricResult(err error) string {
	if err != nil {
		return metricResultError
	}

	return metricResultSuccess
}

func metricSessionStatus(status SessionStatus) string {
	switch status {
	case StatusCreating, StatusRunning, StatusStopped, StatusErrored, StatusDeleting:
		return string(status)
	default:
		return metricLabelUnknown
	}
}

func metricSnapshotKind(kind string) string {
	switch kind {
	case metricSnapshotFull, metricSnapshotDelta:
		return kind
	default:
		return metricLabelUnknown
	}
}

func metricSenderKind(senderID string) string {
	switch {
	case isSystemSender(senderID):
		return metricSenderSystem
	case strings.HasPrefix(senderID, "device:"):
		return metricSenderDevice
	case senderID != "":
		return metricSenderSession
	default:
		return metricLabelUnknown
	}
}

func metricStreamKind(stream string) string {
	switch {
	case strings.HasPrefix(stream, "_system."):
		return metricStreamSystem
	case stream == "":
		return metricLabelUnknown
	}

	if _, ok := parseInboxStream(stream); ok {
		return metricStreamInbox
	}

	return metricStreamTopic
}
