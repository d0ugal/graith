package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/version"
)

const (
	graithUpdateKindUpgrade             = "upgrade"
	graithUpdateKindDowngrade           = "downgrade"
	graithUpdateKindRestartSameVersion  = "restart_without_version_change"
	graithUpdateKindMetadataUnavailable = "version_metadata_unavailable"
)

func currentGraithBuildState(now time.Time) GraithBuildState {
	return GraithBuildState{
		Version:    strings.TrimSpace(version.Version),
		CommitSHA:  strings.TrimSpace(version.CommitSHA),
		ObservedAt: now.UTC(),
	}
}

func sameGraithBuild(a, b GraithBuildState) bool {
	return strings.TrimSpace(a.Version) == strings.TrimSpace(b.Version) &&
		strings.TrimSpace(a.CommitSHA) == strings.TrimSpace(b.CommitSHA)
}

func graithUpdateNotificationFor(previous, current GraithBuildState, detectedAt time.Time) GraithUpdateNotificationState {
	detectedAt = detectedAt.UTC()
	current.ObservedAt = detectedAt

	return GraithUpdateNotificationState{
		ID:         graithUpdateNotificationID(previous, current),
		Kind:       classifyGraithUpdate(previous, current),
		Previous:   previous,
		Current:    current,
		DetectedAt: detectedAt,
	}
}

func graithUpdateNotificationID(previous, current GraithBuildState) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(previous.Version),
		strings.TrimSpace(previous.CommitSHA),
		graithUpdateIDTime(previous.ObservedAt),
		strings.TrimSpace(current.Version),
		strings.TrimSpace(current.CommitSHA),
		graithUpdateIDTime(current.ObservedAt),
	}, "\x00")))

	return "graith-update-" + hex.EncodeToString(sum[:12])
}

func graithUpdateIDTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339Nano)
}

func classifyGraithUpdate(previous, current GraithBuildState) string {
	if cmp, ok := version.Compare(current.Version, previous.Version); ok {
		switch {
		case cmp > 0:
			return graithUpdateKindUpgrade
		case cmp < 0:
			return graithUpdateKindDowngrade
		default:
			return graithUpdateKindRestartSameVersion
		}
	}

	return graithUpdateKindMetadataUnavailable
}

func formatGraithUpdateNotification(notice GraithUpdateNotificationState) string {
	return fmt.Sprintf(`System event: Graith update detected

Previous Graith version: %s
Previous Graith commit: %s
New Graith version: %s
New Graith commit: %s
Detected at: %s
Transition: %s
Event ID: %s

Inspect release notes, configuration changes, and new capabilities when useful. Proactively suggest applicable configuration, workflow, trigger, or skill updates to the user.`,
		formatGraithBuildField(notice.Previous.Version),
		formatGraithBuildField(notice.Previous.CommitSHA),
		formatGraithBuildField(notice.Current.Version),
		formatGraithBuildField(notice.Current.CommitSHA),
		notice.DetectedAt.UTC().Format(time.RFC3339),
		formatGraithUpdateKind(notice.Kind),
		notice.ID,
	)
}

func formatGraithBuildField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "dev" || value == "unknown" {
		return "unavailable"
	}

	return value
}

func formatGraithUpdateKind(kind string) string {
	switch kind {
	case graithUpdateKindUpgrade:
		return "upgrade"
	case graithUpdateKindDowngrade:
		return "downgrade"
	case graithUpdateKindRestartSameVersion:
		return "restart without version change"
	default:
		return "version metadata unavailable"
	}
}

func (sm *SessionManager) recordGraithBuildObservation(current GraithBuildState, detectedAt time.Time) error {
	detectedAt = detectedAt.UTC()
	current.Version = strings.TrimSpace(current.Version)
	current.CommitSHA = strings.TrimSpace(current.CommitSHA)
	current.ObservedAt = detectedAt

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state.GraithBuild == nil {
		sm.state.GraithBuild = &current
		return sm.saveState()
	}

	previous := *sm.state.GraithBuild
	if sameGraithBuild(previous, current) {
		return nil
	}

	if sm.cfg.Orchestrator.Enabled {
		notice := graithUpdateNotificationFor(previous, current, detectedAt)
		if !hasPendingGraithUpdateNotification(sm.state.PendingGraithUpdateNotifications, notice.ID) {
			sm.state.PendingGraithUpdateNotifications = append(sm.state.PendingGraithUpdateNotifications, notice)
		}
	}

	sm.state.GraithBuild = &current

	return sm.saveState()
}

func hasPendingGraithUpdateNotification(notices []GraithUpdateNotificationState, id string) bool {
	for _, notice := range notices {
		if notice.ID == id {
			return true
		}
	}

	return false
}

func (sm *SessionManager) recordCurrentGraithBuildObservation() error {
	now := time.Now().UTC()
	return sm.recordGraithBuildObservation(currentGraithBuildState(now), now)
}

func (sm *SessionManager) deliverPendingGraithUpdateNotifications(ctx context.Context) {
	sm.mu.RLock()
	enabled := sm.cfg.Orchestrator.Enabled
	orchestratorID := sm.findOrchestratorID()
	notices := append([]GraithUpdateNotificationState(nil), sm.state.PendingGraithUpdateNotifications...)
	sm.mu.RUnlock()

	if !enabled || orchestratorID == "" || len(notices) == 0 {
		return
	}

	for _, notice := range notices {
		if err := sm.deliverGraithUpdateNotification(ctx, orchestratorID, notice); err != nil {
			sm.log.Error("failed to deliver Graith update notification", "event_id", notice.ID, "err", err)
			return
		}

		if err := sm.clearPendingGraithUpdateNotification(notice.ID); err != nil {
			sm.log.Error("failed to clear delivered Graith update notification", "event_id", notice.ID, "err", err)
			return
		}
	}
}

func (sm *SessionManager) deliverGraithUpdateNotification(ctx context.Context, orchestratorID string, notice GraithUpdateNotificationState) error {
	if sm.messages == nil {
		return fmt.Errorf("no message store to publish notification to orchestrator %q", orchestratorID)
	}

	stream := "inbox:" + orchestratorID

	existing, err := sm.messages.Read(stream, "", false, notice.ID)
	if err != nil {
		return fmt.Errorf("check existing update notification: %w", err)
	}

	if len(existing) > 0 {
		return nil
	}

	return sm.notifyFromDaemonWithOpts(
		ctx,
		orchestratorID,
		formatGraithUpdateNotification(notice),
		daemonNotificationOpts{threadID: notice.ID},
	)
}

func (sm *SessionManager) clearPendingGraithUpdateNotification(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	pending := sm.state.PendingGraithUpdateNotifications
	for i, notice := range pending {
		if notice.ID == id {
			sm.state.PendingGraithUpdateNotifications = append(pending[:i], pending[i+1:]...)
			return sm.saveState()
		}
	}

	return nil
}
