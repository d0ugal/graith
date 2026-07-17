package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// tracker.go implements the `tracker` trigger action (issue #643): a scheduled
// trigger that polls an issue tracker for active issues and reconciles live
// sessions against them — spawning one session per active issue (seeded from the
// issue body) and reaping the session when its issue leaves the active state.
//
// The tracker is the source of truth; each fire drives the live session set
// toward it. Dedup is by the durable SessionState.TrackerIssue tag, so a restart
// mid-reconcile never double-spawns. See
// docs/design/2026-07-16-tracker-poll-action.md.

// issueRef is one active tracker issue observed in a reconcile pass.
type issueRef struct {
	key    string // stable identity, e.g. "gh:owner/repo#643"
	number int
	title  string
	body   string
	url    string
	labels []string
}

// trackerSession is one non-soft-deleted session owned by a tracker trigger. It
// carries enough status for the reconcile planner to dedup (running/creating/
// stopped/errored all count as "exists") without acting incorrectly on a
// half-created or already-finished session.
type trackerSession struct {
	id       string
	issueKey string
	status   SessionStatus
	// completedCleanly marks a session that self-exited with code 0 — its work is
	// done, so it is not auto-resumed even while its issue stays active (avoids
	// resurrecting a one-shot agent every poll).
	completedCleanly bool
}

func (s trackerSession) running() bool  { return s.status == StatusRunning }
func (s trackerSession) creating() bool { return s.status == StatusCreating }

// live reports whether the session occupies a concurrency slot (running now or
// creating, i.e. about to run).
func (s trackerSession) live() bool { return s.running() || s.creating() }

// trackerReconcilePlan is the reconcile decision for one tracker trigger.
type trackerReconcilePlan struct {
	spawn  []issueRef // active issues with no live session
	resume []string   // session IDs: active issue whose (non-completed) session is stopped/errored
	reap   []string   // session IDs: obsolete issue past the grace window
}

// trackerReapWillAct reports whether applying the reap mode to a session would
// actually change its state. It keeps the concurrency accounting honest: a reap
// the executor won't perform (reap=none, or reap=stop on an already-stopped
// session) must not free a slot.
func trackerReapWillAct(mode string, s trackerSession) bool {
	switch mode {
	case config.TrackerReapNone:
		return false
	case config.TrackerReapDelete:
		return true // SoftDelete works on running/stopped/errored
	default: // stop
		return s.running()
	}
}

// reconcileTracker computes the spawn/resume/reap plan for one tracker trigger.
// It is a pure function so the reconcile logic is exhaustively unit-testable.
//
//	active        — issues currently in the active state (from the poll)
//	existing      — this trigger's sessions (running/creating/stopped/errored)
//	obsoleteSince — per-issue first-seen-obsolete timestamps (issueKey -> time)
//	grace         — how long an issue must stay inactive before its session is reaped
//	maxConcurrent — cap on live tracker sessions (0 = unlimited); bounds spawn+resume
//	reapMode      — stop | delete | none; the planner must know it so a no-op reap
//	                (reap=none, or stop on an already-stopped session) doesn't wrongly
//	                free a concurrency slot
//	now           — current time
//
// It returns the plan, the refreshed obsoleteSince map (rebuilt from the current
// state, so stale entries self-prune), and the number of spawns/resumes the cap
// suppressed (for logging).
func reconcileTracker(
	active []issueRef,
	existing []trackerSession,
	obsoleteSince map[string]time.Time,
	grace time.Duration,
	maxConcurrent int,
	reapMode string,
	now time.Time,
) (trackerReconcilePlan, map[string]time.Time, int) {
	activeSet := make(map[string]bool, len(active))
	for _, iss := range active {
		activeSet[iss.key] = true
	}

	// existingByKey dedups by issue, deterministically preferring a running (then
	// creating) session over a stopped/errored one when duplicates somehow exist.
	existingByKey := make(map[string]trackerSession, len(existing))
	for _, s := range existing {
		cur, ok := existingByKey[s.issueKey]
		if !ok || (!cur.live() && s.live()) {
			existingByKey[s.issueKey] = s
		}
	}

	var plan trackerReconcilePlan

	newGrace := make(map[string]time.Time)
	reaped := make(map[string]bool)

	// Pass 1: reap decisions for obsolete issues.
	for _, s := range existing {
		if activeSet[s.issueKey] {
			continue // active: handled in the spawn/resume pass; clears any grace mark
		}

		firstSeen, ok := obsoleteSince[s.issueKey]
		if !ok {
			firstSeen = now
		}

		newGrace[s.issueKey] = firstSeen

		// A creating session can't be cleanly reaped mid-flight — hold it.
		if s.creating() {
			continue
		}

		if now.Sub(firstSeen) >= grace && trackerReapWillAct(reapMode, s) {
			plan.reap = append(plan.reap, s.id)
			reaped[s.id] = true
		}
	}

	// Pass 2: count slots occupied after the planned reaps.
	occupied := 0

	for _, s := range existing {
		if !reaped[s.id] && s.live() {
			occupied++
		}
	}

	// Pass 3: spawn/resume active issues that have no live session, up to the cap.
	capped := 0

	for _, iss := range active {
		s, ok := existingByKey[iss.key]
		if ok && s.live() {
			continue // already running or being created
		}

		if ok && s.completedCleanly {
			continue // finished its work; don't resurrect it while the issue lingers
		}

		if maxConcurrent > 0 && occupied >= maxConcurrent {
			capped++
			continue
		}

		if ok {
			// Stopped/errored session for a (re-)active issue: resume it rather than
			// spawn a duplicate — mirrors the ensure-reactor auto-resume.
			plan.resume = append(plan.resume, s.id)
		} else {
			plan.spawn = append(plan.spawn, iss)
		}

		occupied++
	}

	return plan, newGrace, capped
}

// actionTracker runs one reconcile pass for a tracker trigger: poll the tracker,
// diff against this trigger's live sessions, then spawn/resume/reap to match.
func (sm *SessionManager) actionTracker(ctx context.Context, t *config.TriggerConfig, fc fireContext) (string, error) {
	tc := t.Action.Tracker
	if tc == nil {
		return "", errors.New("tracker action missing [action.tracker]")
	}

	orchestratorID := sm.orchestratorID()
	if orchestratorID == "" {
		return "", errors.New("no orchestrator session; cannot own spawned sessions")
	}

	repo := tc.RepoPath()
	if repo == "" {
		return "", errors.New("tracker action requires action.tracker.repo")
	}

	// Capture one config snapshot for the whole reconcile pass, so the gh timeout
	// and the lifecycle knobs below all come from a single coherent view (a live
	// reload between reads can't split the pass). The tracker deliberately reuses
	// the pr_watch reader's configured gh timeout (pr_watch.advanced.gh_timeout).
	cfg := sm.Config()

	active, truncated, err := sm.fetchTrackerIssues(ctx, tc, repo, cfg.PRWatch.GHTimeoutDuration())
	if err != nil {
		return "", err
	}

	existing := sm.trackerSessions(t.Name)
	obsoleteSince := sm.trackerObsoleteSnapshot(t.Name)

	plan, newGrace, capped := reconcileTracker(active, existing, obsoleteSince, tc.GraceDuration(), tc.MaxConcurrent, tc.ReapMode(), time.Now())
	sm.setTrackerObsolete(t.Name, newGrace)

	// A capped issue list is not a complete picture: an active issue beyond the
	// fetch limit would look obsolete and be wrongly reaped. Never infer
	// inactivity from a truncated read — skip reaps this pass (spawns are safe).
	if truncated && len(plan.reap) > 0 {
		sm.log.Warn("tracker: issue list hit the fetch limit; skipping reaps this pass",
			"trigger", t.Name, "limit", tc.LimitOr(), "skipped_reaps", len(plan.reap))
		plan.reap = nil
	}

	var failures []string

	spawned := 0

	for _, iss := range plan.spawn {
		//nolint:contextcheck // spawns a PTY-backed session that outlives the fire ctx (matches actionSession).
		if serr := sm.spawnTrackerSession(t, iss, orchestratorID, repo); serr != nil {
			sm.log.Warn("tracker: spawn failed", "trigger", t.Name, "issue", iss.key, "err", serr)
			failures = append(failures, fmt.Sprintf("spawn %s: %v", iss.key, serr))

			continue
		}

		spawned++
	}

	resumed := 0

	lc := cfg.Lifecycle

	for _, id := range plan.resume {
		//nolint:contextcheck // Resume runs its own session lifecycle, detached from the fire ctx.
		if _, rerr := sm.Resume(id, lc.DefaultRowsOrDefault(), lc.DefaultColsOrDefault()); rerr != nil {
			sm.log.Warn("tracker: resume failed", "trigger", t.Name, "session", id, "err", rerr)
			failures = append(failures, fmt.Sprintf("resume %s: %v", id, rerr))

			continue
		}

		resumed++
	}

	reaped := 0

	for _, id := range plan.reap {
		if sm.reapTrackerSession(id, tc.ReapMode()) {
			reaped++
		}
	}

	summary := fmt.Sprintf("active %d, spawned %d, resumed %d, reaped %d", len(active), spawned, resumed, reaped)
	if capped > 0 {
		summary += fmt.Sprintf(", capped %d", capped)
	}

	if t.Action.Deliver != (config.DeliverConfig{}) {
		sm.deliver(ctx, t.Action.Deliver, fmt.Sprintf("Tracker %q: %s", t.Name, summary), repo, sm.triggerVars(t, fc))
	}

	// Surface per-item failures so the run is recorded as errored (LastError,
	// notify) rather than silently reported as a clean reconcile.
	if len(failures) > 0 {
		return summary, fmt.Errorf("%d reconcile action(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}

	return summary, nil
}

// spawnTrackerSession creates one session seeded from an issue, parented to the
// orchestrator and tagged with the trigger + issue key for reconcile dedup.
func (sm *SessionManager) spawnTrackerSession(t *config.TriggerConfig, iss issueRef, orchestratorID, repo string) error {
	prompt, err := config.ExpandTrigger(t.Action.Prompt, trackerIssueVars(t.Name, iss))
	if err != nil {
		return err
	}

	_, err = sm.createTriggerSession(createTriggerReq{
		name:         trackerSessionName(t.Name, iss.number),
		agent:        t.Action.Agent,
		repo:         repo,
		prompt:       prompt,
		model:        t.Action.Model,
		parentID:     orchestratorID,
		triggerName:  t.Name,
		trackerIssue: iss.key,
	})

	return err
}

// reapTrackerSession applies the configured reap policy to an obsolete session.
// It returns true when it changed the session's state, so a no-op (already
// stopped under reap=stop, reap=none, or a protected session) isn't counted or
// retried noisily. Starred and system sessions are never reaped (mirrors
// SoftDelete's guard, extended to the stop path); reap=delete is refused when
// soft delete is disabled so it can never become an immediate hard purge.
func (sm *SessionManager) reapTrackerSession(id, mode string) bool {
	switch mode {
	case config.TrackerReapNone:
		return false
	case config.TrackerReapDelete:
		if sm.cfg.Delete.RetentionDuration() <= 0 {
			// SoftDelete with retention<=0 would produce an already-expired
			// tombstone the purge loop hard-deletes — a hard purge in disguise.
			// Never destroy: skip. (Config validation also rejects this combo.)
			sm.log.Warn("tracker: reap=delete skipped (soft delete disabled, retention=0)", "session", id)
			return false
		}

		if sm.reapProtected(id) {
			return false // starred/system: SoftDelete would error anyway
		}

		if _, err := sm.SoftDelete(id); err != nil {
			sm.log.Warn("tracker: reap (delete) failed", "session", id, "err", err)
			return false
		}

		return true
	default: // stop
		if !sm.reapStopEligible(id) {
			return false // not running, starred, or system: nothing to (or must not) reap
		}

		if err := sm.stopWithReason(id, StopReasonUser, "tracker-reap"); err != nil {
			sm.log.Warn("tracker: reap (stop) failed", "session", id, "err", err)
			return false
		}

		return true
	}
}

// reapProtected reports whether a session must not be reaped (starred or system).
func (sm *SessionManager) reapProtected(id string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s, ok := sm.state.Sessions[id]

	return ok && (s.Starred || IsSystemSession(s))
}

// reapStopEligible reports whether a session can be stopped by a reap: it must be
// running and neither starred nor a system session (re-checked under the lock at
// apply time, since a human could star it between planning and application).
func (sm *SessionManager) reapStopEligible(id string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s, ok := sm.state.Sessions[id]
	if !ok || s.Status != StatusRunning {
		return false
	}

	return !s.Starred && !IsSystemSession(s)
}

// trackerSessions returns the non-soft-deleted sessions this tracker trigger
// owns, tagged by their issue key. It includes creating and errored sessions so
// reconcile dedup survives a restart mid-create (a StatusCreating session is
// marked StatusErrored on reload) — a second poll must not double-spawn.
func (sm *SessionManager) trackerSessions(triggerName string) []trackerSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var out []trackerSession

	for id, s := range sm.state.Sessions {
		if s.TriggerID != triggerName || s.TrackerIssue == "" || s.IsSoftDeleted() {
			continue
		}

		switch s.Status {
		case StatusRunning, StatusStopped, StatusCreating, StatusErrored:
		default:
			continue
		}

		out = append(out, trackerSession{
			id:               id,
			issueKey:         s.TrackerIssue,
			status:           s.Status,
			completedCleanly: trackerCompletedCleanly(s),
		})
	}

	return out
}

// trackerCompletedCleanly reports whether a stopped session finished its work on
// its own (self-exit with code 0) — the same "success" test autoCleanupStopped
// uses: a process that ended without the daemon asking is tagged StopReasonCrash,
// so that bucket at exit 0 is a clean completion (not a user stop / idle / crash).
func trackerCompletedCleanly(s *SessionState) bool {
	if s.Status != StatusStopped || s.StopReason != StopReasonCrash {
		return false
	}

	return s.ExitCode != nil && *s.ExitCode == 0
}

// trackerObsoleteSnapshot copies the current obsolete-since timestamps for a
// trigger (issueKey -> time) out from under the trigger mutex.
func (sm *SessionManager) trackerObsoleteSnapshot(triggerName string) map[string]time.Time {
	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	out := make(map[string]time.Time)
	prefix := triggerName + "\x00"

	for k, v := range sm.triggers.trackerObsolete {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}

	return out
}

// setTrackerObsolete replaces a trigger's obsolete-since entries with the reconcile
// pass's refreshed map (issueKey -> time), pruning any that are no longer obsolete.
func (sm *SessionManager) setTrackerObsolete(triggerName string, m map[string]time.Time) {
	sm.triggers.mu.Lock()
	defer sm.triggers.mu.Unlock()

	prefix := triggerName + "\x00"
	for k := range sm.triggers.trackerObsolete {
		if strings.HasPrefix(k, prefix) {
			delete(sm.triggers.trackerObsolete, k)
		}
	}

	for issueKey, ts := range m {
		sm.triggers.trackerObsolete[trackerGraceKey(triggerName, issueKey)] = ts
	}
}

// trackerIssueVars builds the template variables for an issue-seeded prompt.
func trackerIssueVars(triggerName string, iss issueRef) config.TriggerVars {
	now := time.Now()

	return config.TriggerVars{
		Name:        triggerName,
		Date:        now.Format("2006-01-02"),
		Datetime:    now.Format(time.RFC3339),
		FireTime:    now.Format(time.RFC3339),
		IssueNumber: strconv.Itoa(iss.number),
		IssueTitle:  iss.title,
		IssueBody:   iss.body,
		IssueURL:    iss.url,
		IssueLabels: strings.Join(iss.labels, ", "),
	}
}

// trackerSessionName builds a bounded session name for an issue. The numeric
// suffix is always preserved (the trigger-name prefix is truncated instead), so
// two different issues can never collapse to the same name after truncation.
func trackerSessionName(triggerName string, number int) string {
	const maxLen = 40

	suffix := fmt.Sprintf("-%d", number)

	keep := maxLen - len(suffix)
	if keep < 0 {
		keep = 0
	}

	if len(triggerName) > keep {
		triggerName = triggerName[:keep]
	}

	return triggerName + suffix
}

// --- provider: GitHub Issues (v1) ---

// ghIssue is the JSON shape of one `gh issue list --json ...` item.
type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// fetchTrackerIssues polls the configured provider for its active issues. The
// truncated flag reports that the provider hit the fetch limit — the caller must
// not infer inactivity (reap) from a capped, incomplete read. ghTimeout is the
// per-command deadline captured for this reconcile pass (pr_watch.advanced.gh_timeout).
func (sm *SessionManager) fetchTrackerIssues(ctx context.Context, tc *config.TrackerConfig, repo string, ghTimeout time.Duration) (issues []issueRef, truncated bool, err error) {
	switch tc.ProviderOr() {
	case config.TrackerProviderGitHub:
		return sm.fetchGitHubIssues(ctx, tc, repo, ghTimeout)
	default:
		return nil, false, fmt.Errorf("tracker provider %q is not supported", tc.ProviderOr())
	}
}

// fetchGitHubIssues lists the repo's active issues via the `gh` CLI, reusing the
// pr_watch reader's prompt-disabled runner and its configured per-command timeout
// (pr_watch.advanced.gh_timeout; falls back to the 5s default when non-positive).
// truncated is true when the raw result hit the fetch limit (so the caller skips
// reaps that pass).
func (sm *SessionManager) fetchGitHubIssues(ctx context.Context, tc *config.TrackerConfig, repo string, ghTimeout time.Duration) (issues []issueRef, truncated bool, err error) {
	if !ghAvailable() {
		return nil, false, errors.New("gh CLI not found on PATH")
	}

	slug, ok := repoSlug(repo)
	if !ok {
		return nil, false, fmt.Errorf("no GitHub remote for %q", repo)
	}

	cctx, cancel := context.WithTimeout(ctx, ghTimeoutOr(ghTimeout))
	defer cancel()

	limit := tc.LimitOr()
	args := []string{
		"issue", "list",
		"--repo", slug,
		"--state", tc.ActiveStateOr(),
		"--json", "number,title,body,url,labels",
		"--limit", strconv.Itoa(limit),
	}

	// gh's --label is AND (issue must have every label); the config's active_labels
	// is "has any of these", so filter client-side rather than pass --label. The
	// assignee filter is a single value, so it maps directly.
	if tc.Assignee != "" {
		args = append(args, "--assignee", tc.Assignee)
	}

	out, err := ghRunner(cctx, repo, args...)
	if err != nil {
		return nil, false, fmt.Errorf("gh issue list: %w", err)
	}

	refs, err := parseGitHubIssues(out, slug)
	if err != nil {
		return nil, false, err
	}

	// The limit is applied by gh BEFORE the client-side label filter, so a full
	// page means the label-matching active set may be incomplete — treat it as
	// truncated so the caller doesn't reap on a partial view.
	truncated = len(refs) >= limit

	return filterIssuesByLabels(refs, tc.ActiveLabels), truncated, nil
}

// filterIssuesByLabels keeps issues that carry at least one of the given labels.
// An empty label set is a no-op (all issues pass).
func filterIssuesByLabels(refs []issueRef, labels []string) []issueRef {
	if len(labels) == 0 {
		return refs
	}

	want := make(map[string]bool, len(labels))
	for _, l := range labels {
		want[l] = true
	}

	out := make([]issueRef, 0, len(refs))

	for _, r := range refs {
		for _, l := range r.labels {
			if want[l] {
				out = append(out, r)
				break
			}
		}
	}

	return out
}

// parseGitHubIssues decodes `gh issue list --json` output into issueRefs with a
// stable per-issue key ("gh:<slug>#<number>").
func parseGitHubIssues(out, slug string) ([]issueRef, error) {
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}

	var items []ghIssue
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return nil, fmt.Errorf("parse gh issue list: %w", err)
	}

	refs := make([]issueRef, 0, len(items))

	for _, it := range items {
		labels := make([]string, 0, len(it.Labels))
		for _, l := range it.Labels {
			labels = append(labels, l.Name)
		}

		refs = append(refs, issueRef{
			key:    fmt.Sprintf("gh:%s#%d", slug, it.Number),
			number: it.Number,
			title:  it.Title,
			body:   it.Body,
			url:    it.URL,
			labels: labels,
		})
	}

	return refs, nil
}
