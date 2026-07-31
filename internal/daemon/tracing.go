package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"github.com/d0ugal/graith/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const daemonTracerName = "github.com/d0ugal/graith/internal/daemon"

var errDaemonTracePartialFailure = errors.New("daemon lifecycle operation had partial failures")

//nolint:spancheck // Ownership of the returned span is transferred to the caller.
func startDaemonSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if ctx == nil {
		ctx = context.Background()
	}

	return telemetry.Tracer(daemonTracerName).Start(ctx, name, trace.WithAttributes(attrs...))
}

func endDaemonSpan(span trace.Span, err error) {
	recordDaemonSpanError(span, err)
	span.End()
}

func endDaemonSpanWithFailures(span trace.Span, err error, failures int) {
	if err == nil && failures > 0 {
		err = errDaemonTracePartialFailure
	}

	endDaemonSpan(span, err)
}

func recordDaemonSpanError(span trace.Span, err error) {
	if err == nil {
		return
	}

	errType := daemonTraceErrorType(err)
	span.SetAttributes(attribute.String("error.type", errType))
	span.SetStatus(codes.Error, errType)
}

func daemonTraceErrorType(err error) string {
	if err == nil {
		return ""
	}

	switch {
	case errors.Is(err, context.Canceled):
		return "context.canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context.deadline_exceeded"
	case errors.Is(err, os.ErrNotExist), errors.Is(err, fs.ErrNotExist):
		return "fs.not_exist"
	case errors.Is(err, os.ErrPermission), errors.Is(err, fs.ErrPermission):
		return "fs.permission"
	case errors.Is(err, syscall.ESRCH):
		return "process.not_found"
	case errors.Is(err, syscall.EPERM):
		return "process.permission"
	case errors.Is(err, errDaemonTracePartialFailure):
		return "operation.partial_failure"
	}

	errType := strings.TrimPrefix(fmt.Sprintf("%T", err), "*")
	if errType == "" || errType == "fmt.wrapError" {
		return "error"
	}

	return errType
}

func lifecycleSpanAttrs(driverKind string, sandboxed, mirror, readOnly, inPlace bool, includes int) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("graith.session.driver_kind", lifecycleDriverKind(driverKind)),
		attribute.Bool("graith.session.sandboxed", sandboxed),
		attribute.Bool("graith.session.mirror", mirror),
		attribute.Bool("graith.session.read_only", readOnly),
		attribute.Bool("graith.session.in_place", inPlace),
		attribute.Int("graith.session.includes", includes),
	}
}

func lifecycleDriverKind(driverKind string) string {
	switch driverKind {
	case "", DriverPTY:
		return DriverPTY
	case DriverHeadless:
		return DriverHeadless
	default:
		return "other"
	}
}

func lifecycleInitiator(initiator string) string {
	switch initiator {
	case "restart",
		"watchdog-restart",
		"scenario-policy",
		"soft-delete",
		"soft-delete-orphan",
		"delete",
		"delete-orphan",
		"delete-children",
		"delete-children-orphan",
		"delete-children-sweep",
		"delete-children-sweep-orphan",
		"watchdog-giveup",
		"convert",
		"rollback",
		"create-rollback",
		"resume-rollback",
		"fork-rollback":
		return initiator
	default:
		return "other"
	}
}

func lifecycleStopReason(reason string) string {
	switch reason {
	case StopReasonCrash, StopReasonIdle, StopReasonScenarioTimeout, StopReasonShutdown, StopReasonUser, StopReasonWatchdog,
		StopReasonDelete, StopReasonConvert:
		return reason
	default:
		return "other"
	}
}
