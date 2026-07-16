package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const resourceSampleHistory = 5

// resourceSampleInterval is deliberately coarse: one ps snapshot every 30
// seconds is enough to expose sustained memory/CPU/FD growth without turning
// observability into meaningful daemon overhead. It is a var for tests.
var resourceSampleInterval = 30 * time.Second

var processListOutput = func() ([]byte, error) {
	cmd := exec.Command("/bin/ps", "-axo", "pid=,pgid=,rss=,%cpu=,comm=")
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")

	return cmd.Output()
}

var fdCountReader = openFDCounts

// ResourceSample is a process-group aggregate. A sandboxed session's direct
// child is only the wrapper, so observing the whole group is essential: this
// captures the agent and every tool it spawned rather than the wrapper's RSS.
type ResourceSample struct {
	At           time.Time `json:"at"`
	RSSMB        int64     `json:"rss_mb"`
	CPUPercent   float64   `json:"cpu_percent"`
	OpenFDs      int       `json:"open_fds"`
	FDsPartial   bool      `json:"fds_partial,omitempty"`
	ProcessCount int       `json:"process_count"`
	TopProcess   string    `json:"top_process,omitempty"`
	ProcessIDs   []int     `json:"-"`
}

type signalRequest struct {
	PID       int
	Signal    syscall.Signal
	Initiator string
	At        time.Time
}

type processResource struct {
	pid, pgid int
	rssKB     int64
	cpu       float64
	command   string
}

// RunResourceMonitorLoop periodically snapshots every live session. Sampling
// failures are debug-only and never affect session operation.
func (sm *SessionManager) RunResourceMonitorLoop(ctx context.Context) {
	sm.sampleSessionResources()

	ticker := time.NewTicker(resourceSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sm.resourceKick:
			sm.sampleSessionResources()
		case <-ticker.C:
			sm.sampleSessionResources()
		}
	}
}

func (sm *SessionManager) sampleSessionResources() {
	now := time.Now()

	sm.mu.RLock()

	targets := make(map[int]struct {
		id, name string
	}, len(sm.sessions))
	for id, sess := range sm.sessions {
		if pgid := sess.Pgid(); pgid > 0 && !sess.Exited() {
			// A kick gives a newly launched session an immediate baseline, but it
			// must not replace every established session's five-sample history
			// during a launch burst. Per-session spacing preserves the intended
			// 30-second time series while still sampling new IDs immediately.
			if !sm.resourceSampleDue(id, now) {
				continue
			}

			name := id
			if state := sm.state.Sessions[id]; state != nil {
				name = state.Name
			}

			targets[pgid] = struct{ id, name string }{id, name}
		}
	}

	sm.mu.RUnlock()

	if len(targets) == 0 {
		return
	}

	procs, err := readProcessResources()
	if err != nil {
		sm.log.Debug("session resource sampling failed", "err", err)
		return
	}

	groups := make(map[int][]processResource, len(targets))

	var pids []int

	for _, proc := range procs {
		if _, ok := targets[proc.pgid]; ok {
			groups[proc.pgid] = append(groups[proc.pgid], proc)
			pids = append(pids, proc.pid)
		}
	}

	fdCounts := fdCountReader(pids)

	for pgid, target := range targets {
		members := groups[pgid]
		if len(members) == 0 {
			continue
		}

		sample := ResourceSample{At: now.UTC(), OpenFDs: 0, ProcessCount: len(members)}

		var topRSS int64

		for _, proc := range members {
			sample.RSSMB += proc.rssKB
			sample.CPUPercent += proc.cpu
			sample.ProcessIDs = append(sample.ProcessIDs, proc.pid)

			if n, ok := fdCounts[proc.pid]; ok {
				sample.OpenFDs += n
			} else {
				sample.FDsPartial = true
			}

			if proc.rssKB > topRSS {
				topRSS, sample.TopProcess = proc.rssKB, proc.command
			}
		}

		sample.RSSMB /= 1024

		sm.resourceMu.Lock()
		if sm.resourceSamples == nil {
			sm.resourceSamples = make(map[string][]ResourceSample)
		}

		history := append(sm.resourceSamples[target.id], sample)
		if len(history) > resourceSampleHistory {
			history = history[len(history)-resourceSampleHistory:]
		}

		sm.resourceSamples[target.id] = history
		sm.resourceMu.Unlock()

		sm.log.Debug("session resource sample", "id", target.id, "name", target.name,
			"pgid", pgid, "rss_mb", sample.RSSMB, "cpu_percent", sample.CPUPercent,
			"open_fds", sample.OpenFDs, "process_count", sample.ProcessCount,
			"fds_partial", sample.FDsPartial, "top_process", sample.TopProcess)
	}
}

func (sm *SessionManager) resourceSampleDue(id string, now time.Time) bool {
	sm.resourceMu.Lock()
	defer sm.resourceMu.Unlock()

	history := sm.resourceSamples[id]

	return len(history) == 0 || now.Sub(history[len(history)-1].At) >= resourceSampleInterval
}

func (sm *SessionManager) discardResourceSamples(id string) {
	sm.resourceMu.Lock()
	delete(sm.resourceSamples, id)
	sm.resourceMu.Unlock()
}

func (sm *SessionManager) recordSignalRequest(id string, pid int, signal syscall.Signal, initiator string) {
	if pid <= 0 {
		return
	}

	sm.resourceMu.Lock()
	if sm.signalRequests == nil {
		sm.signalRequests = make(map[string]signalRequest)
	}

	sm.signalRequests[id] = signalRequest{
		PID: pid, Signal: signal, Initiator: initiator, At: time.Now().UTC(),
	}
	sm.resourceMu.Unlock()
}

func (sm *SessionManager) takeSignalRequest(id string, pid int) *signalRequest {
	sm.resourceMu.Lock()
	defer sm.resourceMu.Unlock()

	request, ok := sm.signalRequests[id]
	if !ok || request.PID != pid {
		return nil
	}

	delete(sm.signalRequests, id)

	return &request
}

func (sm *SessionManager) takeResourceSamples(id string, pid int) []ResourceSample {
	sm.resourceMu.Lock()
	defer sm.resourceMu.Unlock()

	history := sm.resourceSamples[id]
	delete(sm.resourceSamples, id)

	if len(history) == 0 || pid == 0 {
		return history
	}

	// Do not attach samples from an older process generation after a rapid
	// restart. The group leader PID is present in every valid sample.
	var matching []ResourceSample

	for i := range history {
		for _, samplePID := range history[i].ProcessIDs {
			if samplePID == pid {
				matching = append(matching, history[i])
				break
			}
		}
	}

	return matching
}

type mcpCrashStatus struct {
	Server  string `json:"server"`
	ProxyID string `json:"proxy_id"`
	PID     int    `json:"pid"`
	Running bool   `json:"running"`
}

// logAbnormalExitReport emits the high-density diagnostic record intended for
// post-mortems. wait(2) reports the terminating signal but not its sender, so a
// matching PID-bound graith request is reported as intent, never as proof of
// provenance; without one, attribution deliberately says external-or-unknown.
func (sm *SessionManager) logAbnormalExitReport(
	id, name, stopReason string,
	sess SessionDriver,
	signalRequest *signalRequest,
) {
	samples := sm.takeResourceSamples(id, sess.ProcessPID())
	if stopReason != StopReasonCrash || (sess.ExitSignal() == 0 && sess.ExitCode() == 0) {
		return
	}

	sm.mu.RLock()
	state := sm.state.Sessions[id]
	attached := sm.attachedClients[id] != nil

	pendingApprovals := 0
	for _, approval := range sm.pendingApprovals {
		if approval.Info.SessionID == id {
			pendingApprovals++
		}
	}

	sandboxed, sandboxBackend := false, ""
	if state != nil {
		sandboxed = state.Sandboxed
		if state.SandboxConfig != nil {
			sandboxBackend = state.SandboxConfig.Backend
		}
	}

	sm.mu.RUnlock()

	category, signalSource := classifyExit(sess.ExitCode(), sess.ExitSignal(), signalRequest)

	lastOutputAge := int64(-1)
	if last := sess.LastOutputAt(); !last.IsZero() {
		lastOutputAge = time.Since(last).Milliseconds()
	}

	unread := 0
	if sm.messages != nil {
		unread = sm.messages.TotalUnread(id)
	}

	var mcpStatuses []mcpCrashStatus

	if sm.mcpManager != nil {
		prefix := id + "-"

		for _, server := range sm.mcpManager.List() {
			for _, conn := range server.Connections {
				if strings.HasPrefix(conn.ProxyID, prefix) {
					mcpStatuses = append(mcpStatuses, mcpCrashStatus{
						Server: server.Name, ProxyID: conn.ProxyID, PID: conn.PID, Running: conn.Running,
					})
				}
			}
		}
	}

	sandboxDiagnostic := "not-sandboxed"
	if sandboxed {
		sandboxDiagnostic = "no wrapper exit-reason API"
		if sandboxBackend == "safehouse" {
			sandboxDiagnostic = "Seatbelt denials are in macOS unified log; run gr sandbox watch --recent 5m " + name
		}
	}

	signalName := ""
	if sig := sess.ExitSignal(); sig != 0 {
		signalName = sig.String()
	}

	sm.log.Error("session abnormal exit report",
		"id", id, "name", name, "category", category,
		"stop_reason", stopReason, "exit_code", sess.ExitCode(),
		"signal", signalName, "signal_source", signalSource,
		"pid", sess.ProcessPID(), "pgid", sess.Pgid(),
		"observed_lifetime_ms", time.Since(sess.CreatedAt()).Milliseconds(),
		"last_output_age_ms", lastOutputAge,
		"resource_samples", samples,
		"sandboxed", sandboxed, "sandbox_backend", sandboxBackend,
		"sandbox_diagnostic", sandboxDiagnostic,
		"attached", attached, "pending_approvals", pendingApprovals,
		"unread_messages", unread, "mcp_processes", mcpStatuses)
}

func classifyExit(exitCode int, signal syscall.Signal, request *signalRequest) (category, signalSource string) {
	if signal != 0 {
		if request != nil && request.Signal == signal {
			return "signal-after-graith-request", "graith-requested"
		}

		return "signal-external-or-unknown", "external-or-unknown"
	}

	if exitCode != 0 {
		return "exit-nonzero", "none"
	}

	return "exit-clean", "none"
}

func readProcessResources() ([]processResource, error) {
	out, err := processListOutput()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}

	return parseProcessResources(string(out)), nil
}

func parseProcessResources(out string) []processResource {
	var resources []processResource

	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		pid, err1 := strconv.Atoi(fields[0])
		pgid, err2 := strconv.Atoi(fields[1])
		rss, err3 := strconv.ParseInt(fields[2], 10, 64)

		cpu, err4 := strconv.ParseFloat(fields[3], 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}

		resources = append(resources, processResource{
			pid: pid, pgid: pgid, rssKB: rss, cpu: cpu,
			command: strings.Join(fields[4:], " "),
		})
	}

	return resources
}
