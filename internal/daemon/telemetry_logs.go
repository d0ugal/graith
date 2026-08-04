package daemon

import (
	"context"
	"syscall"

	"github.com/d0ugal/graith/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

type sessionExitedLogObservation struct {
	SessionID    string
	DriverKind   string
	AgentKind    string
	Status       SessionStatus
	StopReason   string
	ExitCode     int
	PID          int
	PGID         int
	Signal       syscall.Signal
	ExitCategory string
	SignalSource string
	Sandboxed    bool
}

func (sm *SessionManager) emitSessionExitedLogEvent(obs sessionExitedLogObservation) {
	logs := sm.logExporter.Load()
	if logs == nil {
		return
	}

	result := "success"
	if obs.ExitCategory != "exit-clean" {
		result = "error"
	}

	attrs := []attribute.KeyValue{
		attribute.String("session.driver_kind", safeLogDriverKind(obs.DriverKind)),
		attribute.String("session.status", safeLogSessionStatus(obs.Status)),
		attribute.String("session.stop_reason", safeLogStopReason(obs.StopReason)),
		attribute.String("session.exit_category", safeLogExitCategory(obs.ExitCategory)),
		attribute.String("session.signal_source", safeLogSignalSource(obs.SignalSource)),
		attribute.Bool("session.sandboxed", obs.Sandboxed),
		attribute.Int("process.exit_code", obs.ExitCode),
	}
	if ref := logs.ref(obs.SessionID); ref != "" {
		attrs = append(attrs, attribute.String("session.ref", ref))
	}

	attrs = append(attrs, attribute.String("agent_kind", safeLogAgentKind(obs.AgentKind)))

	if obs.PID > 0 {
		attrs = append(attrs, attribute.Int("process.pid", obs.PID))
	}

	if obs.PGID > 0 {
		attrs = append(attrs, attribute.Int("process.pgid", obs.PGID))
	}

	if signal := safeLogSignal(obs.Signal); signal != "" {
		attrs = append(attrs, attribute.String("process.signal", signal))
	}

	if err := logs.emit(context.Background(), telemetry.LogEvent{
		Severity:   telemetry.LogSeverityInfo,
		Domain:     "session",
		Name:       "session.exited",
		Result:     result,
		Attributes: attrs,
	}); err != nil && sm.log != nil {
		sm.log.Warn("telemetry log event rejected", "event", "session.exited", "err", err)
	}
}

func safeLogDriverKind(driverKind string) string {
	switch driverKind {
	case "", DriverPTY:
		return DriverPTY
	case DriverHeadless:
		return DriverHeadless
	default:
		return "unknown"
	}
}

func safeLogSessionStatus(status SessionStatus) string {
	switch status {
	case StatusCreating, StatusRunning, StatusStopped, StatusErrored, StatusDeleting:
		return string(status)
	default:
		return "unknown"
	}
}

func safeLogStopReason(reason string) string {
	switch reason {
	case StopReasonCrash, StopReasonIdle, StopReasonUser, StopReasonShutdown,
		StopReasonWatchdog, StopReasonScenarioTimeout, StopReasonDelete, StopReasonConvert:
		return reason
	default:
		return "unknown"
	}
}

func safeLogExitCategory(category string) string {
	switch category {
	case "exit-clean", "exit-nonzero", "signal-after-graith-request", "signal-external-or-unknown":
		return category
	default:
		return "unknown"
	}
}

func safeLogSignalSource(source string) string {
	switch source {
	case "none", "graith-requested", "external-or-unknown":
		return source
	default:
		return "unknown"
	}
}

func safeLogAgentKind(agent string) string {
	switch agent {
	case "codex", "claude", "cursor", "opencode", "agy":
		return agent
	case "":
		return "unknown"
	default:
		return "custom"
	}
}

func safeLogSignal(sig syscall.Signal) string {
	switch sig {
	case 0:
		return ""
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGALRM:
		return "SIGALRM"
	case syscall.SIGBUS:
		return "SIGBUS"
	case syscall.SIGCHLD:
		return "SIGCHLD"
	case syscall.SIGCONT:
		return "SIGCONT"
	case syscall.SIGFPE:
		return "SIGFPE"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGILL:
		return "SIGILL"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGSTOP:
		return "SIGSTOP"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGTRAP:
		return "SIGTRAP"
	case syscall.SIGTSTP:
		return "SIGTSTP"
	case syscall.SIGTTIN:
		return "SIGTTIN"
	case syscall.SIGTTOU:
		return "SIGTTOU"
	case syscall.SIGURG:
		return "SIGURG"
	case syscall.SIGUSR1:
		return "SIGUSR1"
	case syscall.SIGUSR2:
		return "SIGUSR2"
	case syscall.SIGXCPU:
		return "SIGXCPU"
	case syscall.SIGXFSZ:
		return "SIGXFSZ"
	default:
		return "unknown"
	}
}
