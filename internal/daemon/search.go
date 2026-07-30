package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/d0ugal/graith/internal/agent/transcript"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

const (
	searchTurnOverhead   = 96
	searchCursorVersion  = 1
	searchGenerationLive = "live"
	searchGenerationPrev = "migrated"
	searchStateAll       = "all"
	searchStateActive    = "active"
	searchStateStopped   = "stopped"
)

type conversationSearchCache struct {
	mu      sync.Mutex
	entries map[string]conversationSearchCacheEntry
	bytes   int
}

type conversationSearchCacheEntry struct {
	fingerprint string
	sources     []transcript.Source
	turns       []searchTurn
	truncated   bool
	bytes       int
	accessed    time.Time
}

type searchTurn struct {
	Index     int
	Kind      string
	Text      string
	Timestamp time.Time
}

type searchTarget struct {
	id                   string
	cacheKey             string
	generation           string
	name                 string
	repoPath             string
	repoName             string
	agent                string
	agentSessionID       string
	nativeTranscriptRoot string
	worktreePath         string
	status               SessionStatus
	createdAt            time.Time
	deleted              bool
}

type searchFilters struct {
	query              string
	queryLower         []rune
	sessionID          string
	includeDescendants bool
	repo               string
	agent              string
	kinds              map[string]bool
	since              time.Time
	until              time.Time
	state              string
	includeDeleted     bool
	limit              int
	offset             int
}

type searchCursor struct {
	Version int `json:"v"`
	Offset  int `json:"o"`
}

func newConversationSearchCache() *conversationSearchCache {
	return &conversationSearchCache{entries: make(map[string]conversationSearchCacheEntry)}
}

func (c *conversationSearchCache) get(id, fingerprint string) ([]searchTurn, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[id]
	if !ok || entry.fingerprint != fingerprint {
		return nil, false, false
	}

	entry.accessed = time.Now()
	c.entries[id] = entry

	return cloneSearchTurns(entry.turns), entry.truncated, true
}

func (c *conversationSearchCache) getFresh(id string, target tokenTarget) ([]searchTurn, bool, bool) {
	c.mu.Lock()

	entry, ok := c.entries[id]
	if !ok || len(entry.sources) == 0 {
		c.mu.Unlock()

		return nil, false, false
	}

	fingerprint := entry.fingerprint
	sources := cloneSearchSources(entry.sources)
	c.mu.Unlock()

	refreshed, ok := refreshSearchSources(sources)
	if !ok || tokenFingerprint(target, refreshed) != fingerprint {
		return nil, false, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok = c.entries[id]
	if !ok || entry.fingerprint != fingerprint {
		return nil, false, false
	}

	entry.accessed = time.Now()
	c.entries[id] = entry

	return cloneSearchTurns(entry.turns), entry.truncated, true
}

func (c *conversationSearchCache) put(id, fingerprint string, sources []transcript.Source, turns []searchTurn, truncated bool, limits config.SearchLimits) {
	bytes := searchTurnsBytes(turns)
	if bytes > limits.MaxCacheEntryBytes {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if old, ok := c.entries[id]; ok {
		c.bytes -= old.bytes
	}

	c.entries[id] = conversationSearchCacheEntry{
		fingerprint: fingerprint,
		sources:     cloneSearchSources(sources),
		turns:       cloneSearchTurns(turns),
		truncated:   truncated,
		bytes:       bytes,
		accessed:    time.Now(),
	}
	c.bytes += bytes
	c.pruneLocked(nil, limits)
}

func refreshSearchSources(sources []transcript.Source) ([]transcript.Source, bool) {
	refreshed := make([]transcript.Source, 0, len(sources))
	for _, source := range sources {
		info, err := os.Stat(source.Path)
		if err != nil {
			return nil, false
		}

		refreshed = append(refreshed, transcript.Source{
			Path:    source.Path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	return refreshed, true
}

func (c *conversationSearchCache) prune(live map[string]bool, limits config.SearchLimits) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.pruneLocked(live, limits)
}

func (c *conversationSearchCache) pruneLocked(live map[string]bool, limits config.SearchLimits) {
	for id, entry := range c.entries {
		if live != nil && !live[id] {
			delete(c.entries, id)
			c.bytes -= entry.bytes
		}
	}

	for c.bytes > limits.MaxCacheBytes {
		var (
			oldestID string
			oldest   time.Time
		)

		for id, entry := range c.entries {
			if oldestID == "" || entry.accessed.Before(oldest) {
				oldestID = id
				oldest = entry.accessed
			}
		}

		if oldestID == "" {
			return
		}

		entry := c.entries[oldestID]
		delete(c.entries, oldestID)
		c.bytes -= entry.bytes
	}
}

func (sm *SessionManager) searchCache() *conversationSearchCache {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.search == nil {
		sm.search = newConversationSearchCache()
	}

	return sm.search
}

// SearchConversations searches supported agent transcripts for the given query.
// It snapshots session metadata under sm.mu and performs all filesystem work
// after releasing it.
func (sm *SessionManager) SearchConversations(ctx context.Context, req protocol.SearchMsg) (protocol.SearchResponseMsg, error) {
	limits := sm.Config().Search.Limits()

	filters, err := parseSearchFilters(req, limits)
	if err != nil {
		return protocol.SearchResponseMsg{}, err
	}

	targets, live, err := sm.searchTargets(filters)
	if err != nil {
		return protocol.SearchResponseMsg{}, err
	}

	cache := sm.searchCache()
	cache.prune(live, limits)

	var (
		results         []searchResult
		windowTruncated bool
		unsupported     = make(map[string]int)
	)

	for _, target := range targets {
		select {
		case <-ctx.Done():
			return protocol.SearchResponseMsg{}, ctx.Err()
		default:
		}

		if !transcript.Supported(target.agent) {
			unsupported[target.agent]++
			continue
		}

		turns, parseTruncated, ok, err := sm.cachedSearchTurns(ctx, cache, target, limits)
		if err != nil {
			return protocol.SearchResponseMsg{}, err
		}

		if parseTruncated {
			windowTruncated = true
		}

		if !ok {
			continue
		}

		for _, turn := range turns {
			select {
			case <-ctx.Done():
				return protocol.SearchResponseMsg{}, ctx.Err()
			default:
			}

			if !searchTurnMatchesFilters(turn, filters) {
				continue
			}

			matches := findRuneMatches(turn.Text, filters.queryLower)
			if len(matches) == 0 {
				continue
			}

			results, windowTruncated = appendSearchResult(results, searchResult{
				target:  target,
				turn:    turn,
				matches: matches,
			}, windowTruncated, limits.MaxWindow)
		}
	}

	sortSearchResults(results)

	if len(results) > limits.MaxWindow+1 {
		results = results[:limits.MaxWindow+1]
		windowTruncated = true
	}

	limit := filters.limit

	start := filters.offset
	if start > len(results) {
		start = len(results)
	}

	end := start + limit
	if end > len(results) {
		end = len(results)
	}

	resp := protocol.SearchResponseMsg{
		Results:           make([]protocol.SearchResult, 0, end-start),
		Limit:             limit,
		UnsupportedAgents: unsupportedAgentInfo(unsupported),
	}

	for _, result := range results[start:end] {
		resp.Results = append(resp.Results, result.protocolResult(filters.queryLower, limits))
	}

	if end < len(results) {
		resp.NextCursor = encodeSearchCursor(end)
		resp.Truncated = true
	} else if windowTruncated {
		resp.Truncated = true
	}

	return resp, nil
}

func (sm *SessionManager) cachedSearchTurns(ctx context.Context, cache *conversationSearchCache, target searchTarget, limits config.SearchLimits) ([]searchTurn, bool, bool, error) {
	fingerprintTarget := tokenTarget{
		id:             target.id,
		agent:          target.agent,
		agentSessionID: target.agentSessionID,
		stateRoot:      target.nativeTranscriptRoot,
		worktreePath:   target.worktreePath,
	}

	if target.agentSessionID != "" {
		if turns, truncated, ok := cache.getFresh(target.cacheKey, fingerprintTarget); ok {
			return turns, truncated, true, nil
		}
	}

	sources, err := transcript.LocateWithRoot(target.agent, target.agentSessionID, target.worktreePath, target.nativeTranscriptRoot)
	if err != nil || len(sources) == 0 {
		return nil, false, false, nil
	}

	fp := tokenFingerprint(fingerprintTarget, sources)

	if turns, truncated, ok := cache.get(target.cacheKey, fp); ok {
		return turns, truncated, true, nil
	}

	conv, err := transcript.ReadFromWithOptions(target.agent, sources, transcript.ReadOptions{
		Context:           ctx,
		MaxBytesPerSource: int64(limits.MaxSourceBytes),
		MaxTurnsPerSource: limits.MaxSourceTurns,
	})

	var turns []searchTurn

	truncated := false

	if errors.Is(err, transcript.ErrNoTurns) {
		turns = nil
	} else if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, false, err
		}

		return nil, false, false, nil
	} else {
		truncated = conv.Truncated
		turns = searchTurnsFromConversation(conv, limits.MaxTurnRunes)
	}

	post, err := transcript.LocateWithRoot(target.agent, target.agentSessionID, target.worktreePath, target.nativeTranscriptRoot)
	stable := err == nil && tokenFingerprint(fingerprintTarget, post) == fp

	if stable {
		cache.put(target.cacheKey, fp, post, turns, truncated, limits)
	}

	return turns, truncated, true, nil
}

func (sm *SessionManager) searchTargets(filters searchFilters) ([]searchTarget, map[string]bool, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	live := make(map[string]bool, len(sm.state.Sessions)*2)
	descendants := map[string]bool{}

	if filters.sessionID != "" {
		if _, ok := sm.state.Sessions[filters.sessionID]; !ok {
			return nil, nil, fmt.Errorf("session %q not found", filters.sessionID)
		}

		descendants[filters.sessionID] = true
		if filters.includeDescendants {
			for id := range sm.state.Sessions {
				if id == filters.sessionID || sm.isDescendantOf(id, filters.sessionID) {
					descendants[id] = true
				}
			}
		}
	}

	targets := make([]searchTarget, 0, len(sm.state.Sessions))
	for _, s := range sm.state.Sessions {
		for _, target := range searchTargetsForSession(s) {
			live[target.cacheKey] = true

			if !searchTargetMatchesFilters(target, filters, descendants) {
				continue
			}

			targets = append(targets, target)
		}
	}

	sort.Slice(targets, func(i, j int) bool {
		if !targets[i].createdAt.Equal(targets[j].createdAt) {
			return targets[i].createdAt.After(targets[j].createdAt)
		}

		if targets[i].id != targets[j].id {
			return targets[i].id < targets[j].id
		}

		if targets[i].generation != targets[j].generation {
			return targets[i].generation < targets[j].generation
		}

		if targets[i].agent != targets[j].agent {
			return targets[i].agent < targets[j].agent
		}

		if targets[i].agentSessionID != targets[j].agentSessionID {
			return targets[i].agentSessionID < targets[j].agentSessionID
		}

		return targets[i].cacheKey < targets[j].cacheKey
	})

	return targets, live, nil
}

func searchTargetsForSession(s *SessionState) []searchTarget {
	target := searchTarget{
		id:                   s.ID,
		cacheKey:             searchTargetCacheKey(s.ID, searchGenerationLive, s.Agent, s.AgentSessionID),
		generation:           searchGenerationLive,
		name:                 s.Name,
		repoPath:             s.RepoPath,
		repoName:             s.RepoName,
		agent:                s.Agent,
		agentSessionID:       s.AgentSessionID,
		nativeTranscriptRoot: sessionNativeTranscriptRoot(s),
		worktreePath:         s.WorktreePath,
		status:               s.Status,
		createdAt:            s.CreatedAt,
		deleted:              s.IsSoftDeleted(),
	}

	targets := []searchTarget{target}
	if s.MigratedFrom == nil || s.MigratedFrom.Agent == "" {
		return targets
	}

	if s.MigratedFrom.Agent == s.Agent && s.MigratedFrom.AgentSessionID == s.AgentSessionID {
		return targets
	}

	prev := target
	prev.cacheKey = searchTargetCacheKey(s.ID, searchGenerationPrev, s.MigratedFrom.Agent, s.MigratedFrom.AgentSessionID)
	prev.generation = searchGenerationPrev
	prev.agent = s.MigratedFrom.Agent
	prev.agentSessionID = s.MigratedFrom.AgentSessionID
	prev.nativeTranscriptRoot = s.MigratedFrom.NativeTranscriptRoot

	return append(targets, prev)
}

func searchTargetCacheKey(sessionID, generation, agent, agentSessionID string) string {
	return strings.Join([]string{sessionID, generation, agent, agentSessionID}, "\x00")
}

func parseSearchFilters(req protocol.SearchMsg, limits config.SearchLimits) (searchFilters, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return searchFilters{}, errors.New("search query must not be empty")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = limits.DefaultLimit
	}

	if limit > limits.MaxLimit {
		limit = limits.MaxLimit
	}

	offset, err := decodeSearchCursor(req.Cursor)
	if err != nil {
		return searchFilters{}, err
	}

	if offset > limits.MaxWindow {
		return searchFilters{}, fmt.Errorf("search cursor is beyond the maximum window of %d results", limits.MaxWindow)
	}

	state := req.State
	if state == "" {
		state = searchStateAll
	}

	switch state {
	case searchStateAll, searchStateActive, searchStateStopped:
	default:
		return searchFilters{}, fmt.Errorf("invalid search state %q", req.State)
	}

	since, err := parseSearchTime(req.Since)
	if err != nil {
		return searchFilters{}, fmt.Errorf("invalid --since: %w", err)
	}

	until, err := parseSearchTime(req.Until)
	if err != nil {
		return searchFilters{}, fmt.Errorf("invalid --until: %w", err)
	}

	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return searchFilters{}, errors.New("--until must be at or after --since")
	}

	kinds, err := parseSearchKinds(req.Kinds)
	if err != nil {
		return searchFilters{}, err
	}

	return searchFilters{
		query:              query,
		queryLower:         lowerRunes(query),
		sessionID:          req.SessionID,
		includeDescendants: req.IncludeDescendants,
		repo:               req.Repo,
		agent:              req.Agent,
		kinds:              kinds,
		since:              since,
		until:              until,
		state:              state,
		includeDeleted:     req.IncludeDeleted,
		limit:              limit,
		offset:             offset,
	}, nil
}

func parseSearchTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}

	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts, nil
	}

	if ts, err := time.Parse("2006-01-02", value); err == nil {
		return ts, nil
	}

	return time.Time{}, errors.New("expected RFC3339 timestamp or YYYY-MM-DD date")
}

func parseSearchKinds(values []string) (map[string]bool, error) {
	if len(values) == 0 {
		return nil, nil
	}

	kinds := make(map[string]bool, len(values))
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			kind := strings.ToLower(strings.TrimSpace(part))
			if kind == "" {
				continue
			}

			switch kind {
			case "user", "assistant", "context":
				kinds[kind] = true
			case "tool", "result":
				kinds["tool"] = true
			default:
				return nil, fmt.Errorf("invalid search kind %q", part)
			}
		}
	}

	return kinds, nil
}

func decodeSearchCursor(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}

	if n, err := strconv.Atoi(raw); err == nil {
		if n < 0 {
			return 0, errors.New("search cursor must not be negative")
		}

		return n, nil
	}

	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, errors.New("invalid search cursor")
	}

	var cursor searchCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return 0, errors.New("invalid search cursor")
	}

	if cursor.Version != searchCursorVersion || cursor.Offset < 0 {
		return 0, errors.New("invalid search cursor")
	}

	return cursor.Offset, nil
}

func encodeSearchCursor(offset int) string {
	data, err := json.Marshal(searchCursor{Version: searchCursorVersion, Offset: offset})
	if err != nil {
		return strconv.Itoa(offset)
	}

	return base64.RawURLEncoding.EncodeToString(data)
}

func searchTargetMatchesFilters(target searchTarget, filters searchFilters, descendants map[string]bool) bool {
	if target.deleted && !filters.includeDeleted {
		return false
	}

	if filters.sessionID != "" && !descendants[target.id] {
		return false
	}

	if filters.repo != "" && filters.repo != target.repoPath && filters.repo != target.repoName {
		return false
	}

	if filters.agent != "" && filters.agent != target.agent {
		return false
	}

	switch filters.state {
	case searchStateActive:
		if target.status != StatusRunning && target.status != StatusCreating {
			return false
		}
	case searchStateStopped:
		if target.status != StatusStopped && target.status != StatusErrored {
			return false
		}
	}

	return true
}

func searchTurnMatchesFilters(turn searchTurn, filters searchFilters) bool {
	if len(filters.kinds) > 0 && !filters.kinds[turn.Kind] {
		return false
	}

	if !filters.since.IsZero() || !filters.until.IsZero() {
		if turn.Timestamp.IsZero() {
			return false
		}

		if !filters.since.IsZero() && turn.Timestamp.Before(filters.since) {
			return false
		}

		if !filters.until.IsZero() && turn.Timestamp.After(filters.until) {
			return false
		}
	}

	return true
}

func searchTurnsFromConversation(conv *transcript.Conversation, maxTurnRunes int) []searchTurn {
	turns := make([]searchTurn, 0, len(conv.Turns))
	for i, turn := range conv.Turns {
		text := sanitizeSearchText(searchTextForTurn(turn))
		if strings.TrimSpace(text) == "" {
			continue
		}

		turns = append(turns, searchTurn{
			Index:     i,
			Kind:      string(turn.Role),
			Text:      truncateRunes(text, maxTurnRunes),
			Timestamp: turn.Timestamp,
		})
	}

	return turns
}

func searchTextForTurn(turn transcript.Turn) string {
	if turn.Role != transcript.RoleTool {
		return turn.Text
	}

	if turn.Tool == nil {
		return turn.Text
	}

	var b strings.Builder
	if turn.Tool.Name != "" {
		b.WriteString("Tool call: ")
		b.WriteString(turn.Tool.Name)
	}

	if turn.Tool.Args != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}

		b.WriteString("Arguments:\n")
		b.WriteString(turn.Tool.Args)
	}

	if turn.Tool.Output != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}

		b.WriteString("Output:\n")
		b.WriteString(turn.Tool.Output)
	}

	return b.String()
}

type searchTextState uint8

const (
	searchTextGround searchTextState = iota
	searchTextEscape
	searchTextCSI
	searchTextString
	searchTextStringEscape
)

func sanitizeSearchText(text string) string {
	var (
		b     strings.Builder
		state searchTextState
	)

	for _, r := range text {
		switch state {
		case searchTextGround:
			state = sanitizeSearchTextGround(&b, r)
		case searchTextEscape:
			state = sanitizeSearchTextEscape(r)
		case searchTextCSI:
			state = sanitizeSearchTextCSI(r)
		case searchTextString:
			state = sanitizeSearchTextString(r)
		case searchTextStringEscape:
			state = sanitizeSearchTextStringEscape(r)
		}
	}

	return b.String()
}

func sanitizeSearchTextGround(b *strings.Builder, r rune) searchTextState {
	switch {
	case r == '\x1b':
		return searchTextEscape
	case r == '\u009b':
		return searchTextCSI
	case startsTerminalControlString(r):
		return searchTextString
	case r == '\n' || r == '\t':
		b.WriteRune(r)
	case r == '\r':
		b.WriteByte('\n')
	case unicode.IsControl(r):
		return searchTextGround
	default:
		b.WriteRune(r)
	}

	return searchTextGround
}

func sanitizeSearchTextEscape(r rune) searchTextState {
	switch r {
	case '[':
		return searchTextCSI
	case ']', 'P', '_', '^', 'X':
		return searchTextString
	default:
		if r >= 0x30 && r <= 0x7e {
			return searchTextGround
		}

		return searchTextEscape
	}
}

func sanitizeSearchTextCSI(r rune) searchTextState {
	if r >= 0x40 && r <= 0x7e {
		return searchTextGround
	}

	return searchTextCSI
}

func sanitizeSearchTextString(r rune) searchTextState {
	switch r {
	case '\a', '\u009c':
		return searchTextGround
	case '\x1b':
		return searchTextStringEscape
	default:
		return searchTextString
	}
}

func sanitizeSearchTextStringEscape(r rune) searchTextState {
	if r == '\\' {
		return searchTextGround
	}

	if r == '\x1b' {
		return searchTextStringEscape
	}

	return searchTextString
}

func startsTerminalControlString(r rune) bool {
	switch r {
	case '\u0090', '\u0098', '\u009d', '\u009e', '\u009f':
		return true
	default:
		return false
	}
}

type runeRange struct {
	start int
	end   int
}

func findRuneMatches(text string, queryLower []rune) []runeRange {
	if len(queryLower) == 0 {
		return nil
	}

	textLower := lowerRunes(text)

	var matches []runeRange

	for i := 0; i+len(queryLower) <= len(textLower); {
		if equalRunes(textLower[i:i+len(queryLower)], queryLower) {
			matches = append(matches, runeRange{start: i, end: i + len(queryLower)})
			i += len(queryLower)

			continue
		}

		i++
	}

	return matches
}

func lowerRunes(text string) []rune {
	out := []rune(text)
	for i, r := range out {
		out[i] = unicode.ToLower(r)
	}

	return out
}

func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func truncateRunes(text string, maxRunes int) string {
	if len(text) <= maxRunes {
		return text
	}

	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}

	return string(runes[:maxRunes])
}

type searchResult struct {
	target  searchTarget
	turn    searchTurn
	matches []runeRange
}

func sortSearchResults(results []searchResult) {
	sort.SliceStable(results, func(i, j int) bool {
		left := results[i]
		right := results[j]

		var (
			leftTime  = left.sortTime()
			rightTime = right.sortTime()
		)

		if !leftTime.Equal(rightTime) {
			return leftTime.After(rightTime)
		}

		if left.target.id != right.target.id {
			return left.target.id < right.target.id
		}

		if left.target.generation != right.target.generation {
			return left.target.generation < right.target.generation
		}

		if left.target.agent != right.target.agent {
			return left.target.agent < right.target.agent
		}

		if left.target.agentSessionID != right.target.agentSessionID {
			return left.target.agentSessionID < right.target.agentSessionID
		}

		return left.turn.Index < right.turn.Index
	})
}

func appendSearchResult(results []searchResult, result searchResult, truncated bool, maxWindow int) ([]searchResult, bool) {
	results = append(results, result)
	if len(results) <= (maxWindow+1)*2 {
		return results, truncated
	}

	sortSearchResults(results)

	return results[:maxWindow+1], true
}

func (r searchResult) sortTime() time.Time {
	if !r.turn.Timestamp.IsZero() {
		return r.turn.Timestamp
	}

	return r.target.createdAt
}

func (r searchResult) protocolResult(queryLower []rune, limits config.SearchLimits) protocol.SearchResult {
	snippet, matches := buildSearchSnippet(r.turn.Text, r.matches, queryLower, limits.SnippetRunes, limits.SnippetContext)

	result := protocol.SearchResult{
		SessionID:      r.target.id,
		SessionName:    r.target.name,
		RepoPath:       r.target.repoPath,
		RepoName:       r.target.repoName,
		Agent:          r.target.agent,
		AgentSessionID: r.target.agentSessionID,
		Kind:           r.turn.Kind,
		Snippet:        snippet,
		Matches:        matches,
		Locator:        r.locator(),
	}

	if !r.turn.Timestamp.IsZero() {
		result.Timestamp = r.turn.Timestamp.UTC().Format(time.RFC3339Nano)
	}

	return result
}

func (r searchResult) locator() string {
	parts := []string{
		"s", r.target.id,
		"a", r.target.agent,
		"n", r.target.agentSessionID,
		"t", strconv.Itoa(r.turn.Index),
	}

	return strings.Join(parts, ":")
}

func buildSearchSnippet(text string, matches []runeRange, queryLower []rune, snippetRunes, snippetContext int) (string, []protocol.SearchMatchRange) {
	runes := []rune(text)
	if len(runes) == 0 || len(matches) == 0 {
		return "", nil
	}

	first := matches[0]
	start := first.start - snippetContext

	if start < 0 {
		start = 0
	}

	end := start + snippetRunes
	if end > len(runes) {
		end = len(runes)

		start = end - snippetRunes
		if start < 0 {
			start = 0
		}
	}

	prefix := ""
	if start > 0 {
		prefix = "..."
	}

	suffix := ""
	if end < len(runes) {
		suffix = "..."
	}

	windowRunes := runes[start:end]
	snippet := prefix + string(windowRunes) + suffix
	prefixRunes := len([]rune(prefix))

	var ranges []protocol.SearchMatchRange
	for _, match := range findRuneMatches(string(windowRunes), queryLower) {
		ranges = append(ranges, protocol.SearchMatchRange{
			Start: prefixRunes + match.start,
			End:   prefixRunes + match.end,
		})
	}

	return snippet, ranges
}

func searchTurnsBytes(turns []searchTurn) int {
	total := 0
	for _, turn := range turns {
		total += len(turn.Text) + searchTurnOverhead
	}

	return total
}

func cloneSearchTurns(turns []searchTurn) []searchTurn {
	if len(turns) == 0 {
		return nil
	}

	out := make([]searchTurn, len(turns))
	copy(out, turns)

	return out
}

func cloneSearchSources(sources []transcript.Source) []transcript.Source {
	if len(sources) == 0 {
		return nil
	}

	out := make([]transcript.Source, len(sources))
	copy(out, sources)

	return out
}

func unsupportedAgentInfo(counts map[string]int) []protocol.SearchUnsupportedAgent {
	if len(counts) == 0 {
		return nil
	}

	agents := make([]string, 0, len(counts))
	for agent := range counts {
		agents = append(agents, agent)
	}

	sort.Strings(agents)

	out := make([]protocol.SearchUnsupportedAgent, 0, len(agents))
	for _, agent := range agents {
		out = append(out, protocol.SearchUnsupportedAgent{Agent: agent, Count: counts[agent]})
	}

	return out
}
