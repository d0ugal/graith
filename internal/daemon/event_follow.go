package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
)

const (
	eventTypeSessionEvent = "session_event"
	eventFollowClassCI    = "ci"
)

var allowedEventFollowClasses = map[string]struct{}{
	eventFollowClassCI: {},
}

type ciFollowDelivery struct {
	sourceID     string
	sourceName   string
	parentID     string
	parentName   string
	branch       string
	slug         string
	prNumber     int
	prURL        string
	headRefOID   string
	state        string
	failing      []string
	pending      int
	passed       int
	total        int
	dedupKey     string
	prevDedupKey string
	hadPrevDedup bool
	deliveredAt  time.Time
	messageBody  string
}

func normalizeEventFollowClasses(events []string, require bool) ([]string, error) {
	seen := make(map[string]struct{})

	for _, raw := range events {
		for _, part := range strings.Split(raw, ",") {
			event := strings.ToLower(strings.TrimSpace(part))
			if event == "" {
				continue
			}

			if _, ok := allowedEventFollowClasses[event]; !ok {
				return nil, fmt.Errorf("unsupported event class %q (allowed: %s)", event, allowedEventFollowClassList())
			}

			seen[event] = struct{}{}
		}
	}

	if len(seen) == 0 {
		if require {
			return nil, fmt.Errorf("at least one event class is required (allowed: %s)", allowedEventFollowClassList())
		}

		return nil, nil
	}

	out := make([]string, 0, len(seen))
	for event := range seen {
		out = append(out, event)
	}

	sort.Strings(out)

	return out, nil
}

func allowedEventFollowClassList() string {
	events := make([]string, 0, len(allowedEventFollowClasses))
	for event := range allowedEventFollowClasses {
		events = append(events, event)
	}

	sort.Strings(events)

	return strings.Join(events, ",")
}

func cloneEventFollowRule(rule *EventFollowRuleState) *EventFollowRuleState {
	if rule == nil {
		return nil
	}

	clone := *rule

	clone.Events = append([]string{}, rule.Events...)
	if rule.LastDelivered != nil {
		clone.LastDelivered = make(map[string]string, len(rule.LastDelivered))
		for event, key := range rule.LastDelivered {
			clone.LastDelivered[event] = key
		}
	}

	return &clone
}

func (sm *SessionManager) validateEventFollowParentLocked(parentID string) error {
	if parentID == "" {
		return errors.New("--follow-events requires a direct parent session")
	}

	parent, ok := sm.state.Sessions[parentID]
	if !ok {
		return fmt.Errorf("parent session %q not found", parentID)
	}

	if parent.Status == StatusCreating {
		return fmt.Errorf("parent session %q is being created", parent.Name)
	}

	if parent.Status == StatusDeleting || parent.IsSoftDeleted() {
		return fmt.Errorf("parent session %q is being deleted", parent.Name)
	}

	if parent.SystemKind == SystemKindOrchestrator {
		return errors.New("the config-managed system orchestrator cannot follow child events")
	}

	return nil
}

func (sm *SessionManager) authorizeEventFollowRuleLocked(auth authContext, childID string) (*SessionState, *SessionState, error) {
	child, ok := sm.state.Sessions[childID]
	if !ok {
		return nil, nil, fmt.Errorf("child session %q not found", childID)
	}

	if child.Status == StatusDeleting {
		return nil, nil, fmt.Errorf("child session %q is being deleted", child.Name)
	}

	if child.Status == StatusCreating {
		return nil, nil, fmt.Errorf("child session %q is being created", child.Name)
	}

	if child.IsSoftDeleted() {
		return nil, nil, errSoftDeleted(child.Name)
	}

	if child.ParentID == "" {
		return nil, nil, fmt.Errorf("child session %q has no direct parent", child.Name)
	}

	parent, ok := sm.state.Sessions[child.ParentID]
	if !ok {
		return nil, nil, fmt.Errorf("parent session %q not found", child.ParentID)
	}

	if parent.Status == StatusCreating {
		return nil, nil, fmt.Errorf("parent session %q is being created", parent.Name)
	}

	if parent.Status == StatusDeleting || parent.IsSoftDeleted() {
		return nil, nil, fmt.Errorf("parent session %q is being deleted", parent.Name)
	}

	if parent.SystemKind == SystemKindOrchestrator {
		return nil, nil, errors.New("the config-managed system orchestrator cannot follow child events")
	}

	switch {
	case auth.isLocalHuman():
		return child, parent, nil
	case auth.authenticated && auth.sessionID == parent.ID:
		return child, parent, nil
	default:
		return nil, nil, errors.New("not authorized: only the direct parent session or local user may manage event forwarding")
	}
}

func (sm *SessionManager) FollowEvents(auth authContext, childID string, events []string) (protocol.EventFollowRuleInfo, error) {
	normalized, err := normalizeEventFollowClasses(events, true)
	if err != nil {
		return protocol.EventFollowRuleInfo{}, err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	child, parent, err := sm.authorizeEventFollowRuleLocked(auth, childID)
	if err != nil {
		return protocol.EventFollowRuleInfo{}, err
	}

	if sm.state.EventFollowRules == nil {
		sm.state.EventFollowRules = make(map[string]*EventFollowRuleState)
	}

	now := time.Now().UTC()
	beforeRule := cloneEventFollowRule(sm.state.EventFollowRules[child.ID])

	rule := sm.state.EventFollowRules[child.ID]
	if rule == nil {
		rule = &EventFollowRuleState{
			SourceSessionID: child.ID,
			CreatedAt:       now,
			LastDelivered:   make(map[string]string),
		}
		sm.state.EventFollowRules[child.ID] = rule
	}

	rule.Events = append([]string{}, normalized...)
	rule.UpdatedAt = now
	pruneLastDelivered(rule)

	if err := sm.saveState(); err != nil {
		restoreEventFollowRuleLocked(sm.state.EventFollowRules, child.ID, beforeRule)
		return protocol.EventFollowRuleInfo{}, err
	}

	return eventFollowRuleInfo(child, parent, rule), nil
}

func (sm *SessionManager) UnfollowEvents(auth authContext, childID string, events []string) (protocol.EventFollowRuleInfo, error) {
	normalized, err := normalizeEventFollowClasses(events, false)
	if err != nil {
		return protocol.EventFollowRuleInfo{}, err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	child, parent, err := sm.authorizeEventFollowRuleLocked(auth, childID)
	if err != nil {
		return protocol.EventFollowRuleInfo{}, err
	}

	rule := sm.state.EventFollowRules[child.ID]
	if rule == nil {
		return protocol.EventFollowRuleInfo{}, fmt.Errorf("parent is not following events from child session %q", child.Name)
	}

	beforeRule := cloneEventFollowRule(rule)
	infoRule := cloneEventFollowRule(rule)

	if len(normalized) == 0 {
		delete(sm.state.EventFollowRules, child.ID)

		infoRule.Events = nil
		infoRule.LastDelivered = nil
	} else {
		remaining := removeEventFollowClasses(rule.Events, normalized)
		if len(remaining) == 0 {
			delete(sm.state.EventFollowRules, child.ID)

			infoRule.Events = nil
			infoRule.LastDelivered = nil
		} else {
			rule.Events = remaining
			rule.UpdatedAt = time.Now().UTC()
			pruneLastDelivered(rule)
			infoRule = cloneEventFollowRule(rule)
		}
	}

	if err := sm.saveState(); err != nil {
		restoreEventFollowRuleLocked(sm.state.EventFollowRules, child.ID, beforeRule)
		return protocol.EventFollowRuleInfo{}, err
	}

	return eventFollowRuleInfo(child, parent, infoRule), nil
}

func restoreEventFollowRuleLocked(rules map[string]*EventFollowRuleState, childID string, rule *EventFollowRuleState) {
	if rule == nil {
		delete(rules, childID)
		return
	}

	rules[childID] = rule
}

func removeEventFollowClasses(current, remove []string) []string {
	removeSet := make(map[string]struct{}, len(remove))

	for _, event := range remove {
		removeSet[event] = struct{}{}
	}

	var remaining []string

	for _, event := range current {
		if _, drop := removeSet[event]; !drop {
			remaining = append(remaining, event)
		}
	}

	sort.Strings(remaining)

	return remaining
}

func pruneLastDelivered(rule *EventFollowRuleState) {
	if rule.LastDelivered == nil {
		return
	}

	for event := range rule.LastDelivered {
		if !slices.Contains(rule.Events, event) {
			delete(rule.LastDelivered, event)
		}
	}
}

func (sm *SessionManager) EventFollowing(auth authContext) ([]protocol.EventFollowRuleInfo, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var infos []protocol.EventFollowRuleInfo

	for childID, rule := range sm.state.EventFollowRules {
		child := sm.state.Sessions[childID]
		if child == nil {
			continue
		}

		parent := sm.state.Sessions[child.ParentID]
		if parent == nil || parent.SystemKind == SystemKindOrchestrator {
			continue
		}

		if !auth.isLocalHuman() {
			if !auth.authenticated || auth.sessionID != parent.ID {
				continue
			}
		}

		infos = append(infos, eventFollowRuleInfo(child, parent, rule))
	}

	sort.Slice(infos, func(i, j int) bool {
		if infos[i].ParentSessionID != infos[j].ParentSessionID {
			return infos[i].ParentSessionID < infos[j].ParentSessionID
		}

		return infos[i].ChildSessionID < infos[j].ChildSessionID
	})

	return infos, nil
}

func eventFollowRuleInfo(child, parent *SessionState, rule *EventFollowRuleState) protocol.EventFollowRuleInfo {
	info := protocol.EventFollowRuleInfo{}

	if rule != nil {
		info.Events = append([]string{}, rule.Events...)
		info.CreatedAt = eventAt(rule.CreatedAt)
		info.UpdatedAt = eventAt(rule.UpdatedAt)
	}

	if child != nil {
		info.ChildSessionID = child.ID
		info.ChildSession = child.Name
	} else if rule != nil {
		info.ChildSessionID = rule.SourceSessionID
	}

	if parent != nil {
		info.ParentSessionID = parent.ID
		info.ParentSession = parent.Name
	}

	return info
}

func (sm *SessionManager) handleReparentedEventFollowRuleLocked(sourceID, newParentID string) {
	rule := sm.state.EventFollowRules[sourceID]
	if rule == nil {
		return
	}

	if newParentID == "" {
		delete(sm.state.EventFollowRules, sourceID)
		return
	}

	parent := sm.state.Sessions[newParentID]
	if parent == nil || parent.SystemKind == SystemKindOrchestrator ||
		parent.Status == StatusCreating || parent.Status == StatusDeleting || parent.IsSoftDeleted() {
		delete(sm.state.EventFollowRules, sourceID)
		return
	}

	rule.UpdatedAt = time.Now().UTC()
}

func ciFollowDedupKey(d prData) string {
	head := strings.TrimSpace(d.HeadRefOid)
	if head == "" {
		head = "unknown-head"
	}

	return fmt.Sprintf("pr:%d:head:%s:state:%s", d.Number, head, d.CIState)
}

func isForwardableCIState(state string) bool {
	return state == "pending" || state == "failing" || state == "passing"
}

func (sm *SessionManager) deliverFollowedCIEvent(ctx context.Context, t prWatchTarget, slug string, d prData) {
	delivery, ok := sm.reserveFollowedCIEvent(t, slug, d)
	if !ok {
		return
	}

	body := followedCIBody(delivery)
	delivery.messageBody = body

	if err := sm.notifyFromDaemonWithOpts(ctx, delivery.parentID, body, daemonNotificationOpts{}); err != nil {
		sm.rollbackFollowedCIDelivery(delivery)
		return
	}

	sm.publishFollowedCIEvent(delivery)
}

func (sm *SessionManager) reserveFollowedCIEvent(t prWatchTarget, slug string, d prData) (ciFollowDelivery, bool) {
	if !isForwardableCIState(d.CIState) {
		return ciFollowDelivery{}, false
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	rule := sm.state.EventFollowRules[t.id]
	if rule == nil || !slices.Contains(rule.Events, eventFollowClassCI) {
		return ciFollowDelivery{}, false
	}

	source := sm.state.Sessions[t.id]
	if source == nil || source.Status == StatusDeleting || source.IsSoftDeleted() {
		delete(sm.state.EventFollowRules, t.id)
		_ = sm.saveState()

		return ciFollowDelivery{}, false
	}

	parent := sm.state.Sessions[source.ParentID]
	if parent == nil || parent.Status == StatusCreating || parent.Status == StatusDeleting || parent.IsSoftDeleted() {
		delete(sm.state.EventFollowRules, t.id)
		_ = sm.saveState()

		return ciFollowDelivery{}, false
	}

	if parent.SystemKind == SystemKindOrchestrator {
		delete(sm.state.EventFollowRules, t.id)
		_ = sm.saveState()

		return ciFollowDelivery{}, false
	}

	key := ciFollowDedupKey(d)
	if rule.LastDelivered != nil && rule.LastDelivered[eventFollowClassCI] == key {
		return ciFollowDelivery{}, false
	}

	if rule.LastDelivered == nil {
		rule.LastDelivered = make(map[string]string)
	}

	prevKey, hadPrevKey := rule.LastDelivered[eventFollowClassCI]
	prevUpdatedAt := rule.UpdatedAt
	now := time.Now().UTC()
	rule.LastDelivered[eventFollowClassCI] = key
	rule.UpdatedAt = now

	if err := sm.saveState(); err != nil {
		if hadPrevKey {
			rule.LastDelivered[eventFollowClassCI] = prevKey
		} else {
			delete(rule.LastDelivered, eventFollowClassCI)
		}

		rule.UpdatedAt = prevUpdatedAt

		sm.log.Error("failed to persist followed CI cursor", "session", t.id, "err", err)

		return ciFollowDelivery{}, false
	}

	return ciFollowDelivery{
		sourceID:     source.ID,
		sourceName:   source.Name,
		parentID:     parent.ID,
		parentName:   parent.Name,
		branch:       t.branch,
		slug:         slug,
		prNumber:     d.Number,
		prURL:        d.URL,
		headRefOID:   d.HeadRefOid,
		state:        d.CIState,
		failing:      slices.Clone(d.FailingChecks),
		pending:      d.CIPending,
		passed:       d.CIPassed,
		total:        d.CITotal,
		dedupKey:     key,
		prevDedupKey: prevKey,
		hadPrevDedup: hadPrevKey,
		deliveredAt:  now,
	}, true
}

func (sm *SessionManager) rollbackFollowedCIDelivery(d ciFollowDelivery) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	rule := sm.state.EventFollowRules[d.sourceID]
	if rule == nil || rule.LastDelivered == nil {
		return
	}

	if rule.LastDelivered[eventFollowClassCI] != d.dedupKey {
		return
	}

	if d.hadPrevDedup {
		rule.LastDelivered[eventFollowClassCI] = d.prevDedupKey
	} else {
		delete(rule.LastDelivered, eventFollowClassCI)
	}

	rule.UpdatedAt = time.Now().UTC()
	_ = sm.saveState()
}

func (sm *SessionManager) publishFollowedCIEvent(d ciFollowDelivery) {
	if sm.events == nil {
		return
	}

	sm.events.Publish(protocol.EventMsg{
		Type:                 eventTypeSessionEvent,
		At:                   eventAt(d.deliveredAt),
		SessionID:            d.parentID,
		Session:              d.parentName,
		Forwarded:            true,
		EventClass:           eventFollowClassCI,
		SourceSessionID:      d.sourceID,
		SourceSession:        d.sourceName,
		DestinationSessionID: d.parentID,
		DestinationSession:   d.parentName,
		PRNumber:             d.prNumber,
		PRURL:                d.prURL,
		HeadRefOID:           d.headRefOID,
		CIState:              d.state,
		FailingChecks:        append([]string{}, d.failing...),
		CIPending:            d.pending,
		CIPassed:             d.passed,
		CITotal:              d.total,
		System:               true,
		// This body is daemon-composed from structured PR/check state only.
		// Future forwarded event classes must preserve the existing trust
		// boundary and avoid placing raw external content here.
		Body: d.messageBody,
	})
}

func followedCIBody(d ciFollowDelivery) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Forwarded child session event: CI is %s for child %q (%s) on PR #%d",
		d.state, d.sourceName, d.sourceID, d.prNumber)

	if d.branch != "" {
		fmt.Fprintf(&b, " (%s)", d.branch)
	}

	b.WriteString(".")

	if d.prURL != "" {
		fmt.Fprintf(&b, "\nPR: %s", d.prURL)
	}

	if d.headRefOID != "" {
		head := d.headRefOID
		if len(head) > 12 {
			head = head[:12]
		}

		fmt.Fprintf(&b, "\nHead SHA: %s", head)
	}

	if d.total > 0 {
		fmt.Fprintf(&b, "\nChecks: %d/%d passing", d.passed, d.total)
	}

	if d.pending > 0 {
		fmt.Fprintf(&b, "\nPending checks: %d", d.pending)
	}

	if len(d.failing) > 0 {
		b.WriteString("\nFailing checks:")

		for _, check := range d.failing {
			fmt.Fprintf(&b, "\n- %s", check)
		}
	}

	if d.slug != "" {
		fmt.Fprintf(&b, "\nInspect: `gh pr checks %d --repo %s`", d.prNumber, d.slug)
	}

	b.WriteString("\nThis is a daemon-authored forwarded event from the child session; it does not change this parent's own PR or CI state.")

	return b.String()
}
