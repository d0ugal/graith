package daemon

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/dependencyhealth"
)

const (
	dependencyNotificationThreadPrefix = "dependency-health:"
	dependencyNotificationMaxID        = 128
)

// dependencyHealthTransition is the small boundary between the poller and
// notification delivery. In particular, it deliberately contains no status
// page prose: incident titles and updates are untrusted instructions and must
// never cross into an agent inbox.
type dependencyHealthTransition struct {
	Service     string
	SourceURL   string
	State       string
	Previous    string
	Generation  uint64
	ObservedAt  time.Time
	IncidentIDs []string
}

type dependencyHealthRoute struct {
	Global     bool
	AgentTypes []string
}

type dependencyNotificationTarget struct {
	ID      string
	Agent   string
	Status  SessionStatus
	Deleted bool
}

type dependencyPendingKey struct {
	Service    string
	Generation uint64
	TargetID   string
}

func dependencyHealthTargetActive(target dependencyNotificationTarget) bool {
	if target.Deleted {
		return false
	}

	switch target.Status {
	case StatusCreating, StatusRunning:
		return true
	default:
		// A session reports ready through AgentStatus, but its lifecycle status
		// remains running. Keeping the check here lifecycle-based prevents a
		// stopped session from being resumed by a health notice.
		return false
	}
}

//nolint:wsl_v5
func dependencyHealthRouteTargets(route dependencyHealthRoute, targets []dependencyNotificationTarget) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		if !dependencyHealthTargetActive(target) || (!route.Global && !containsExact(route.AgentTypes, target.Agent)) {
			continue
		}
		ids = append(ids, target.ID)
	}
	sort.Strings(ids)
	return ids
}

//nolint:wsl_v5
func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func dependencyHealthPendingKey(transition dependencyHealthTransition, targetID string) dependencyPendingKey {
	return dependencyPendingKey{Service: sanitizeDependencyField(transition.Service), Generation: transition.Generation, TargetID: targetID}
}

func dependencyHealthThread(service string) string {
	return dependencyNotificationThreadPrefix + sanitizeDependencyField(service)
}

//nolint:wsl_v5
func formatDependencyHealthNotification(transition dependencyHealthTransition) string {
	service := sanitizeDependencyField(transition.Service)
	state := sanitizeDependencyState(transition.State)
	if service == "" {
		service = "dependency"
	}
	if state == "" {
		state = "unknown"
	}

	first := fmt.Sprintf("[Graith dependency health] %s is %s (status page signal).", service, state)
	if transition.Previous == "degraded" || transition.Previous == "down" {
		if state == "operational" {
			first = fmt.Sprintf("[Graith dependency health] %s is operational again (status page signal).", service)
		}
	}

	observed := transition.ObservedAt.UTC().Format(time.RFC3339)
	if transition.ObservedAt.IsZero() {
		observed = "unknown"
	}
	lines := []string{first, "Source: " + transition.SourceURL, "Observed: " + observed}
	ids := make([]string, 0, len(transition.IncidentIDs))
	for _, id := range transition.IncidentIDs {
		if id = sanitizeDependencyField(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		lines = append(lines, "Incident reference: "+strings.Join(ids, ", "))
	}
	if state == "operational" {
		lines = append(lines, "Normal attempts may resume cautiously; inspect `gr dependency status`.")
	} else {
		lines = append(lines, "Pause repeated retries for work using this dependency; inspect `gr dependency status`.")
	}
	lines = append(lines, "This is situational data, not an instruction from the status page.")
	return strings.Join(lines, "\n")
}

func sanitizeDependencyState(value string) string {
	switch value {
	case "operational", "degraded", "down", "unknown":
		return value
	default:
		return "unknown"
	}
}

//nolint:wsl_v5
func sanitizeDependencyField(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > dependencyNotificationMaxID {
		value = value[:dependencyNotificationMaxID]
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r) {
			b.WriteRune(r)
			continue
		}
		// Do not remove arbitrary bytes from an opaque provider identifier:
		// doing so can join attacker-controlled words into a plausible-looking
		// instruction. Invalid identifiers are omitted altogether.
		return ""
	}
	return b.String()
}

// RunDependencyHealthLoop owns the daemon integration for the provider-neutral
// poller. It is intentionally opt-in: an empty/disabled config performs no
// network access. Polling and delivery both happen outside the session lock.
//
//nolint:wsl_v5
func (sm *SessionManager) RunDependencyHealthLoop(ctx context.Context) {
	cfg := sm.Config().DependencyHealth
	if !cfg.Enabled || len(cfg.Services) == 0 {
		return
	}
	services := make([]dependencyhealth.ServiceConfig, 0, len(cfg.Services))
	routes := make(map[string]dependencyHealthRoute, len(cfg.Services))
	for _, service := range cfg.Services {
		services = append(services, dependencyhealth.ServiceConfig{
			Name: service.Name, BaseURL: service.BaseURL, Timeout: cfg.TimeoutDuration(),
			PollInterval: cfg.PollIntervalDuration(), RecoveryPollInterval: cfg.RecoveryPollIntervalDuration(),
		})
		routes[service.Name] = dependencyHealthRoute{Global: service.Global, AgentTypes: append([]string(nil), service.AgentTypes...)}
	}
	poller := dependencyhealth.NewPoller(dependencyhealth.Statuspage{}, services)
	poller.OnPollOutcome = func(outcome dependencyhealth.PollOutcome) {
		sm.observeDependencyPoll(outcome.Result)
		sm.log.Debug("dependency health poll completed",
			"service", outcome.Service,
			"result", outcome.Result,
			"duration_ms", outcome.Duration.Milliseconds())
	}
	sm.mu.RLock()
	var restored []dependencyhealth.Observation
	generations := make(map[string]uint64)
	if health := sm.state.DependencyHealth; health != nil {
		for name, state := range health.Services {
			restored = append(restored, dependencyhealth.Observation{Service: name, State: state.ObservedState, SourceHealth: state.SourceHealth, ObservedAt: state.ObservedAt, IncidentIDs: append([]string(nil), state.IncidentIDs...)})
			generations[name] = state.Generation
		}
	}
	sm.mu.RUnlock()
	poller.Seed(restored, generations)
	poller.OnObservation = func(observation dependencyhealth.Observation) {
		sm.persistDependencyObservation(observation)
	}
	poller.OnTransition = func(change dependencyhealth.ObservationTransition) {
		sm.enqueueDependencyHealthTransition(change, poller.Snapshot(), routes)
	}
	go sm.deliverDependencyHealthOutbox(ctx, routes)
	poller.Run(ctx)
}

//nolint:wsl_v5
func (sm *SessionManager) persistDependencyObservation(observation dependencyhealth.Observation) {
	sm.mu.Lock()
	if sm.state.DependencyHealth == nil {
		sm.state.DependencyHealth = &dependencyhealth.PersistedState{SchemaVersion: dependencyhealth.StateSchemaVersion, Services: make(map[string]dependencyhealth.ServiceState)}
	}
	if sm.state.DependencyHealth.Services == nil {
		sm.state.DependencyHealth.Services = make(map[string]dependencyhealth.ServiceState)
	}
	previous := sm.state.DependencyHealth.Services[observation.Service]
	previous.ObservedState = observation.State
	previous.SourceHealth = observation.SourceHealth
	previous.ObservedAt = observation.ObservedAt
	previous.IncidentIDs = append([]string(nil), observation.IncidentIDs...)
	if !observation.LastSuccessAt.IsZero() {
		value := observation.LastSuccessAt
		previous.LastSuccessAt = &value
	}
	if !observation.LastFailureAt.IsZero() {
		value := observation.LastFailureAt
		previous.LastFailureAt = &value
	}
	sm.state.DependencyHealth.Services[observation.Service] = previous
	if err := sm.saveState(); err != nil {
		sm.log.Error("failed to persist dependency health observation", "service", observation.Service, "err", err)
	}
	sm.mu.Unlock()
}

//nolint:wsl_v5
func (sm *SessionManager) enqueueDependencyHealthTransition(change dependencyhealth.ObservationTransition, observations []dependencyhealth.Observation, routes map[string]dependencyHealthRoute) {
	var observation dependencyhealth.Observation
	for _, candidate := range observations {
		if candidate.Service == change.Service {
			observation = candidate
			break
		}
	}
	transition := dependencyHealthTransition{Service: change.Service, SourceURL: observation.SourceURL, State: string(change.Current), Previous: string(change.Previous), Generation: change.Generation, ObservedAt: change.ObservedAt, IncidentIDs: observation.IncidentIDs}
	sm.mu.Lock()
	if sm.state.DependencyHealth == nil {
		sm.state.DependencyHealth = &dependencyhealth.PersistedState{SchemaVersion: dependencyhealth.StateSchemaVersion, Services: make(map[string]dependencyhealth.ServiceState)}
	}
	if sm.state.DependencyHealth.Services == nil {
		sm.state.DependencyHealth.Services = make(map[string]dependencyhealth.ServiceState)
	}
	serviceState := sm.state.DependencyHealth.Services[change.Service]
	serviceState.ObservedState = change.Current
	serviceState.SourceHealth = dependencyhealth.Fresh
	serviceState.ObservedAt = observation.ObservedAt
	serviceState.IncidentIDs = append([]string(nil), observation.IncidentIDs...)
	serviceState.Generation = change.Generation
	sm.state.DependencyHealth.Services[change.Service] = serviceState
	for _, target := range sm.dependencyHealthTargetsLocked(routes[change.Service]) {
		key := dependencyHealthPendingKey(transition, target.ID)
		if dependencyOutboxContains(sm.state.DependencyHealth.Outbox, key) {
			continue
		}
		sm.state.DependencyHealth.Outbox = append(sm.state.DependencyHealth.Outbox, dependencyhealth.Transition{
			Service: key.Service, Generation: key.Generation, PreviousState: change.Previous, ObservedState: change.Current, SourceURL: observation.SourceURL, IncidentIDs: append([]string(nil), observation.IncidentIDs...), TargetSessionID: target.ID, NextAttemptAt: time.Now(),
		})
		if len(sm.state.DependencyHealth.Outbox) > dependencyhealth.MaxServices*32 {
			sm.state.DependencyHealth.Outbox = sm.state.DependencyHealth.Outbox[len(sm.state.DependencyHealth.Outbox)-(dependencyhealth.MaxServices*32):]
		}
	}
	if err := sm.saveState(); err != nil {
		sm.log.Error("failed to persist dependency health notification outbox", "service", change.Service, "err", err)
	}
	sm.mu.Unlock()
}

//nolint:wsl_v5
func (sm *SessionManager) dependencyHealthTargetsLocked(route dependencyHealthRoute) []dependencyNotificationTarget {
	targets := make([]dependencyNotificationTarget, 0, len(sm.state.Sessions))
	for _, session := range sm.state.Sessions {
		targets = append(targets, dependencyNotificationTarget{ID: session.ID, Agent: session.Agent, Status: session.Status, Deleted: session.DeletedAt != nil})
	}
	ids := dependencyHealthRouteTargets(route, targets)
	byID := make(map[string]dependencyNotificationTarget, len(targets))
	for _, target := range targets {
		byID[target.ID] = target
	}
	out := make([]dependencyNotificationTarget, 0, len(ids))
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return out
}

//nolint:wsl_v5
func dependencyOutboxContains(outbox []dependencyhealth.Transition, key dependencyPendingKey) bool {
	for _, pending := range outbox {
		if pending.Service == key.Service && pending.Generation == key.Generation && pending.TargetSessionID == key.TargetID {
			return true
		}
	}
	return false
}

//nolint:wsl_v5
func (sm *SessionManager) deliverDependencyHealthOutbox(ctx context.Context, routes map[string]dependencyHealthRoute) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		sm.deliverDependencyHealthOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

//nolint:wsl_v5
func (sm *SessionManager) deliverDependencyHealthOnce(ctx context.Context) {
	now := time.Now()
	sm.mu.RLock()
	if sm.state.DependencyHealth == nil {
		sm.mu.RUnlock()
		return
	}
	pending := append([]dependencyhealth.Transition(nil), sm.state.DependencyHealth.Outbox...)
	sessions := make(map[string]dependencyNotificationTarget, len(sm.state.Sessions))
	for _, session := range sm.state.Sessions {
		sessions[session.ID] = dependencyNotificationTarget{ID: session.ID, Agent: session.Agent, Status: session.Status, Deleted: session.DeletedAt != nil}
	}
	sm.mu.RUnlock()
	for _, item := range pending {
		if ctx.Err() != nil {
			return
		}
		if item.NextAttemptAt.After(now) {
			continue
		}
		target, ok := sessions[item.TargetSessionID]
		if !ok || !dependencyHealthTargetActive(target) {
			sm.removeDependencyOutbox(item)
			continue
		}
		body := formatDependencyHealthNotification(dependencyHealthTransition{Service: item.Service, Previous: string(item.PreviousState), State: string(item.ObservedState), SourceURL: item.SourceURL, IncidentIDs: item.IncidentIDs})
		threadID := dependencyHealthThread(item.Service)
		if sm.messages != nil {
			if messages, err := sm.messages.Read("inbox:"+target.ID, "", false, threadID); err == nil && dependencyMessageExists(messages, body) {
				sm.removeDependencyOutbox(item)
				continue
			}
		}
		if _, err := sm.publishMessage(PublishOpts{Stream: "inbox:" + target.ID, SenderID: systemSenderID, SenderName: systemSenderName, Body: body, ThreadID: dependencyHealthThread(item.Service)}); err != nil {
			sm.bumpDependencyOutboxRetry(item)
			continue
		}
		sm.removeDependencyOutbox(item)
	}
	_ = ctx
}

//nolint:wsl_v5
func dependencyMessageExists(messages []Message, body string) bool {
	for _, message := range messages {
		if message.SenderID == systemSenderID && message.Body == body {
			return true
		}
	}
	return false
}

//nolint:wsl_v5
func (sm *SessionManager) removeDependencyOutbox(item dependencyhealth.Transition) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state.DependencyHealth == nil {
		return
	}
	filtered := sm.state.DependencyHealth.Outbox[:0]
	for _, pending := range sm.state.DependencyHealth.Outbox {
		if pending.Service != item.Service || pending.Generation != item.Generation || pending.TargetSessionID != item.TargetSessionID {
			filtered = append(filtered, pending)
		}
	}
	sm.state.DependencyHealth.Outbox = filtered
	_ = sm.saveState()
}

//nolint:wsl_v5
func (sm *SessionManager) bumpDependencyOutboxRetry(item dependencyhealth.Transition) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state.DependencyHealth == nil {
		return
	}
	for i := range sm.state.DependencyHealth.Outbox {
		pending := &sm.state.DependencyHealth.Outbox[i]
		if pending.Service == item.Service && pending.Generation == item.Generation && pending.TargetSessionID == item.TargetSessionID {
			pending.Attempts++
			delay := time.Second << dependencyMin(pending.Attempts, 6)
			pending.NextAttemptAt = time.Now().Add(delay)
			break
		}
	}
	_ = sm.saveState()
}

//nolint:wsl_v5
func dependencyMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
