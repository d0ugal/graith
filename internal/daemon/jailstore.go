package daemon

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

// JailedComment is a PR comment that pr_watch blocked as untrusted and
// quarantined instead of discarding (issue #1082). It carries enough metadata
// for the human/orchestrator to inspect the comment and decide whether to
// release it (deliver it to the target session) or leave it jailed.
type JailedComment struct {
	ID            string `json:"id"`
	CommentID     int64  `json:"comment_id"`
	Surface       string `json:"surface"` // "inline review" | "conversation"
	PRNumber      int    `json:"pr_number"`
	RepoSlug      string `json:"repo_slug,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Author        string `json:"author"`
	Association   string `json:"association,omitempty"`
	IsBot         bool   `json:"is_bot,omitempty"`
	Path          string `json:"path,omitempty"`
	Line          int    `json:"line,omitempty"`
	Body          string `json:"body"`
	TargetSession string `json:"target_session"`
	TargetName    string `json:"target_name,omitempty"`
	JailedAt      string `json:"jailed_at"`
	ReleasedAt    string `json:"released_at,omitempty"`
}

// Released reports whether this jailed comment has already been released.
func (j JailedComment) Released() bool { return j.ReleasedAt != "" }

func generateJailID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)

	return "jail_" + hex.EncodeToString(b)
}

// Jail quarantines a blocked PR comment. It assigns and returns the generated
// jail ID and is idempotent on (comment_id, surface, target_session): a repeat
// of the same comment leaves the existing row untouched and returns its
// existing ID with created=false.
func (s *MsgStore) Jail(j JailedComment) (id string, created bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if j.JailedAt == "" {
		j.JailedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}

	j.ID = generateJailID()

	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO jailed_comments
		 (id, comment_id, surface, pr_number, repo_slug, branch, author, association,
		  is_bot, path, line, body, target_session, target_name, jailed_at, released_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
		j.ID, j.CommentID, j.Surface, j.PRNumber, j.RepoSlug, j.Branch, j.Author, j.Association,
		boolToInt(j.IsBot), j.Path, j.Line, j.Body, j.TargetSession, j.TargetName, j.JailedAt,
	)
	if err != nil {
		return "", false, fmt.Errorf("jail comment: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		// A row already existed for this (comment_id, surface, target_session);
		// fetch its ID so the caller can reference it.
		var existing string

		row := s.db.QueryRow(
			`SELECT id FROM jailed_comments WHERE comment_id = ? AND surface = ? AND target_session = ?`,
			j.CommentID, j.Surface, j.TargetSession)
		if err := row.Scan(&existing); err != nil {
			return "", false, fmt.Errorf("jail comment: lookup existing: %w", err)
		}

		return existing, false, nil
	}

	return j.ID, true, nil
}

const jailCols = `id, comment_id, surface, pr_number, repo_slug, branch, author, association,
	is_bot, path, line, body, target_session, target_name, jailed_at, released_at`

func scanJailed(rows *sql.Rows) (JailedComment, error) {
	var (
		j     JailedComment
		isBot int
	)

	err := rows.Scan(&j.ID, &j.CommentID, &j.Surface, &j.PRNumber, &j.RepoSlug, &j.Branch,
		&j.Author, &j.Association, &isBot, &j.Path, &j.Line, &j.Body,
		&j.TargetSession, &j.TargetName, &j.JailedAt, &j.ReleasedAt)
	if err != nil {
		return JailedComment{}, err
	}

	j.IsBot = isBot != 0

	return j, nil
}

// ListJailed returns quarantined comments, newest first, capped at the
// configured jail list limit (default 2000) so a long-running daemon with
// retention disabled can't be made to serialize an unbounded result set (mirrors
// the msg_conversation clamp; newest-first, so the cap drops the oldest). When
// includeReleased is false, already-released entries are excluded.
func (s *MsgStore) ListJailed(includeReleased bool) ([]JailedComment, error) {
	limit := s.jailListLimit
	if limit < 1 {
		limit = config.MessagesJailListLimitDefault
	}

	q := `SELECT ` + jailCols + ` FROM jailed_comments`
	if !includeReleased {
		q += ` WHERE released_at = ''`
	}

	// The limit is a config-resolved integer (Validate caps it at the ceiling),
	// never user input, so formatting it into the SQL is safe.
	q += fmt.Sprintf(` ORDER BY jailed_at DESC, id DESC LIMIT %d`, limit) //nolint:gosec // G202: LIMIT is a config-resolved int, not user input

	rows, err := s.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("list jailed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []JailedComment

	for rows.Next() {
		j, err := scanJailed(rows)
		if err != nil {
			return nil, fmt.Errorf("scan jailed: %w", err)
		}

		out = append(out, j)
	}

	return out, rows.Err()
}

// GetJailed returns a single quarantined comment by jail ID. Reads don't take
// the mutex here (mirrors ListJailed), so it shares getJailedLocked's body.
func (s *MsgStore) GetJailed(id string) (JailedComment, bool, error) {
	return s.getJailedLocked(id)
}

// MarkReleased stamps a jailed comment as released. It returns ok=false if the
// entry does not exist or was already released (so a double-release can't
// re-deliver the same comment). The returned entry is the pre-release snapshot,
// used by the caller to build the delivery.
func (s *MsgStore) MarkReleased(id string) (JailedComment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	j, found, err := s.getJailedLocked(id)
	if err != nil || !found || j.Released() {
		return j, false, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`UPDATE jailed_comments SET released_at = ? WHERE id = ? AND released_at = ''`, now, id); err != nil {
		return JailedComment{}, false, fmt.Errorf("mark released: %w", err)
	}

	return j, true, nil
}

// Unrelease reverts a release stamp back to unreleased. Used to un-claim a
// release whose delivery failed, so it stays retryable rather than stuck
// released-but-undelivered. Returns ok=true if a row was actually reverted.
func (s *MsgStore) Unrelease(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(`UPDATE jailed_comments SET released_at = '' WHERE id = ? AND released_at <> ''`, id)
	if err != nil {
		return false, fmt.Errorf("unrelease: %w", err)
	}

	n, _ := res.RowsAffected()

	return n > 0, nil
}

// getJailedLocked runs the single-row jail lookup without taking s.mu. It is
// safe under a held lock (MarkReleased calls it while holding s.mu) and safe
// lock-free (GetJailed calls it directly, like the other read-only queries) —
// the "Locked" suffix means it does no locking of its own, not that a caller
// must hold the mutex.
func (s *MsgStore) getJailedLocked(id string) (JailedComment, bool, error) {
	rows, err := s.db.Query(`SELECT `+jailCols+` FROM jailed_comments WHERE id = ?`, id)
	if err != nil {
		return JailedComment{}, false, fmt.Errorf("get jailed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		return JailedComment{}, false, rows.Err()
	}

	j, err := scanJailed(rows)
	if err != nil {
		return JailedComment{}, false, fmt.Errorf("scan jailed: %w", err)
	}

	return j, true, nil
}

// UnreleasedJailed returns all not-yet-released quarantined comments. Used by
// auto-release to re-evaluate trust against a freshly-reloaded config.
func (s *MsgStore) UnreleasedJailed() ([]JailedComment, error) {
	return s.ListJailed(false)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}

	return 0
}
