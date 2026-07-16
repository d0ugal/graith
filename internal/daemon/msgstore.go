package daemon

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
	_ "modernc.org/sqlite"
)

type Message struct {
	ID         string `json:"id"`
	Seq        int64  `json:"seq"`
	Stream     string `json:"stream"`
	SenderID   string `json:"sender_id"`
	SenderName string `json:"sender_name,omitempty"`
	Body       string `json:"body"`
	ThreadID   string `json:"thread_id,omitempty"`
	ReplyTo    string `json:"reply_to,omitempty"`
	CreatedAt  string `json:"created_at"`
	// System marks a daemon-authored automated notification (PR/CI notices,
	// etc.) as distinct from an LLM/session/human message. It is derived from
	// the sender ID rather than stored, so it is set when messages are read or
	// published, not persisted as a column. See issue #887.
	System bool `json:"system,omitempty"`
}

type StreamInfo struct {
	Name     string `json:"name"`
	Total    int64  `json:"total"`
	Unread   int64  `json:"unread"`
	LatestAt string `json:"latest_at,omitempty"`
}

type MsgStore struct {
	db   *sql.DB
	mu   sync.Mutex
	subs map[string][]chan Message
	// subscriberBuffer is the capacity of each per-subscriber channel; jailListLimit
	// bounds a jail listing. Both are resolved from config at open time (issue #1249).
	subscriberBuffer int
	jailListLimit    int
}

// MsgStoreSettings carries the config-derived operational limits for the message
// store. A zero value resolves each field to its built-in default, so tests and
// other callers that don't tune these can pass MsgStoreSettings{} (or nothing).
type MsgStoreSettings struct {
	BusyTimeout      time.Duration
	SubscriberBuffer int
	JailListLimit    int
}

func (s MsgStoreSettings) resolved() MsgStoreSettings {
	if s.BusyTimeout <= 0 {
		s.BusyTimeout = config.MessagesBusyTimeoutDefault
	}

	if s.SubscriberBuffer < 1 {
		s.SubscriberBuffer = config.MessagesSubscriberBufferDefault
	}

	if s.JailListLimit < 1 {
		s.JailListLimit = config.MessagesJailListLimitDefault
	}

	return s
}

// NewMsgStore opens (creating if needed) the message database at dbPath. An
// optional MsgStoreSettings tunes the SQLite busy timeout, the per-subscriber
// channel buffer, and the jail listing cap; omit it (or pass a zero value) to
// use the built-in defaults. Only the first settings value is used.
func NewMsgStore(dbPath string, settings ...MsgStoreSettings) (*MsgStore, error) {
	var st MsgStoreSettings
	if len(settings) > 0 {
		st = settings[0]
	}

	st = st.resolved()

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("create messages db dir: %w", err)
	}

	dsn := fmt.Sprintf("%s?_pragma=journal_mode(wal)&_pragma=busy_timeout(%d)", dbPath, st.BusyTimeout.Milliseconds())

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open messages db: %w", err)
	}

	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &MsgStore{
		db:               db,
		subs:             make(map[string][]chan Message),
		subscriberBuffer: st.SubscriberBuffer,
		jailListLimit:    st.JailListLimit,
	}, nil
}

func initSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id          TEXT PRIMARY KEY,
			seq         INTEGER NOT NULL,
			stream      TEXT NOT NULL,
			sender_id   TEXT NOT NULL,
			sender_name TEXT NOT NULL DEFAULT '',
			body        TEXT NOT NULL,
			thread_id   TEXT,
			reply_to    TEXT,
			created_at  TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_messages_stream_seq ON messages(stream, seq);
		CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(thread_id) WHERE thread_id IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages(created_at);
		CREATE INDEX IF NOT EXISTS idx_messages_sender ON messages(sender_id);

		CREATE TABLE IF NOT EXISTS cursors (
			subscriber TEXT NOT NULL,
			stream     TEXT NOT NULL,
			ack_seq    INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (subscriber, stream)
		);

		CREATE TABLE IF NOT EXISTS acked_messages (
			subscriber TEXT NOT NULL,
			stream     TEXT NOT NULL,
			seq        INTEGER NOT NULL,
			acked_at   TEXT NOT NULL,
			PRIMARY KEY (subscriber, stream, seq)
		);

		CREATE TABLE IF NOT EXISTS stream_hwm (
			stream  TEXT PRIMARY KEY,
			max_seq INTEGER NOT NULL DEFAULT 0
		);

		INSERT OR IGNORE INTO stream_hwm (stream, max_seq)
		SELECT stream, MAX(seq) FROM messages GROUP BY stream;

		-- jailed_comments holds PR comments that pr_watch blocked as untrusted
		-- (issue #1082). Rather than discard the content, it is quarantined here
		-- with its metadata so the human/orchestrator can inspect and release it.
		-- The UNIQUE(comment_id, surface, target_session) constraint makes jailing
		-- idempotent: a re-fetch of the same comment (e.g. a degraded re-prime)
		-- can't create a duplicate row.
		CREATE TABLE IF NOT EXISTS jailed_comments (
			id             TEXT PRIMARY KEY,
			comment_id     INTEGER NOT NULL,
			surface        TEXT NOT NULL,
			pr_number      INTEGER NOT NULL,
			repo_slug      TEXT NOT NULL DEFAULT '',
			branch         TEXT NOT NULL DEFAULT '',
			author         TEXT NOT NULL DEFAULT '',
			association    TEXT NOT NULL DEFAULT '',
			is_bot         INTEGER NOT NULL DEFAULT 0,
			path           TEXT NOT NULL DEFAULT '',
			line           INTEGER NOT NULL DEFAULT 0,
			body           TEXT NOT NULL DEFAULT '',
			target_session TEXT NOT NULL DEFAULT '',
			target_name    TEXT NOT NULL DEFAULT '',
			jailed_at      TEXT NOT NULL,
			released_at    TEXT NOT NULL DEFAULT '',
			UNIQUE(comment_id, surface, target_session)
		);
		CREATE INDEX IF NOT EXISTS idx_jailed_author ON jailed_comments(author);
		CREATE INDEX IF NOT EXISTS idx_jailed_jailed_at ON jailed_comments(jailed_at);
	`)
	if err != nil {
		return fmt.Errorf("init messages schema: %w", err)
	}

	return nil
}

func generateMsgID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)

	return "msg_" + hex.EncodeToString(b)
}

// PublishOpts describes a message to publish to a stream. It replaces the six
// consecutive positional string parameters of Publish — same-typed positional
// strings are the highest-risk transposition case, and the trailing ThreadID /
// ReplyTo are frequently empty, so a struct with named fields is both safer and
// less noisy at the call site.
type PublishOpts struct {
	// Stream is the target stream (topic, inbox:<id>, or system stream).
	Stream string
	// SenderID is the sender's session ID (or a system sender ID).
	SenderID string
	// SenderName is the sender's human-readable name.
	SenderName string
	// Body is the message body.
	Body string
	// ThreadID groups the message into a conversation thread (optional).
	ThreadID string
	// ReplyTo is the stream this message replies to (optional).
	ReplyTo string
}

func (s *MsgStore) Publish(opts PublishOpts) (Message, error) {
	stream := opts.Stream
	senderID := opts.SenderID
	senderName := opts.SenderName
	body := opts.Body
	threadID := opts.ThreadID
	replyTo := opts.ReplyTo

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var seq int64

	err = tx.QueryRow(`
		SELECT MAX(next) + 1 FROM (
			SELECT COALESCE(MAX(seq), 0) AS next FROM messages WHERE stream = ?
			UNION ALL
			SELECT COALESCE(MAX(max_seq), 0) AS next FROM stream_hwm WHERE stream = ?
		)`, stream, stream).Scan(&seq)
	if err != nil {
		return Message{}, fmt.Errorf("next seq: %w", err)
	}

	_, err = tx.Exec(
		`INSERT INTO stream_hwm (stream, max_seq) VALUES (?, ?)
		 ON CONFLICT(stream) DO UPDATE SET max_seq = MAX(stream_hwm.max_seq, excluded.max_seq)`,
		stream, seq,
	)
	if err != nil {
		return Message{}, fmt.Errorf("update hwm: %w", err)
	}

	msg := Message{
		ID:         generateMsgID(),
		Seq:        seq,
		Stream:     stream,
		SenderID:   senderID,
		SenderName: senderName,
		Body:       body,
		ThreadID:   threadID,
		ReplyTo:    replyTo,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		System:     isSystemSender(senderID),
	}

	_, err = tx.Exec(
		`INSERT INTO messages (id, seq, stream, sender_id, sender_name, body, thread_id, reply_to, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.Seq, msg.Stream, msg.SenderID, msg.SenderName, msg.Body,
		nullStr(msg.ThreadID), nullStr(msg.ReplyTo), msg.CreatedAt,
	)
	if err != nil {
		return Message{}, fmt.Errorf("insert message: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("commit: %w", err)
	}

	subs := make([]chan Message, len(s.subs[stream]))
	copy(subs, s.subs[stream])

	go func() {
		for _, ch := range subs {
			select {
			case ch <- msg:
			default:
			}
		}
	}()

	return msg, nil
}

func (s *MsgStore) Read(stream, subscriber string, onlyUnread bool, threadID string) ([]Message, error) {
	var args []any

	q := "SELECT id, seq, stream, sender_id, sender_name, body, COALESCE(thread_id, ''), COALESCE(reply_to, ''), created_at FROM messages WHERE stream = ?"

	args = append(args, stream)

	if onlyUnread && subscriber != "" {
		q += " AND seq > COALESCE((SELECT ack_seq FROM cursors WHERE subscriber = ? AND stream = ?), 0)"

		args = append(args, subscriber, stream)
		q += " AND seq NOT IN (SELECT seq FROM acked_messages WHERE subscriber = ? AND stream = ?)"

		args = append(args, subscriber, stream)
	}

	if threadID != "" {
		q += " AND thread_id = ?"

		args = append(args, threadID)
	}

	q += " ORDER BY seq ASC"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var msgs []Message

	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Seq, &m.Stream, &m.SenderID, &m.SenderName, &m.Body, &m.ThreadID, &m.ReplyTo, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}

		m.System = isSystemSender(m.SenderID)

		msgs = append(msgs, m)
	}

	return msgs, rows.Err()
}

// Conversation returns every direct message involving `self`, both directions:
// messages delivered to self's inbox (stream = "inbox:"+self) and messages self
// sent to any peer's inbox (sender_id = self AND an inbox: stream other than
// self's own). Topic messages are excluded — a "conversation" is direct
// messages only. Results are ordered by created_at, with id as a deterministic
// tie-breaker (seq is per-stream, so it is not a usable cross-stream order key).
//
// When limit > 0, the most recent `limit` messages are returned (still in
// ascending order). The query reads inbox streams the caller may not own; the
// daemon authorises the target via checkTarget before calling this, and the
// sender_id filter ensures the outbound branch only returns messages the target
// session actually authored.
func (s *MsgStore) Conversation(self string, limit int) ([]Message, error) {
	inbox := "inbox:" + self
	// Inner query selects both directions; the OR de-duplicates a row that
	// could match both branches (it cannot here, because the outbound branch
	// excludes self's own inbox). GLOB is case-sensitive so it can use the
	// stream index, unlike LIKE which SQLite treats case-insensitively.
	const cols = `id, seq, stream, sender_id, sender_name, body, thread_id, reply_to, created_at`

	inner := `
		SELECT id, seq, stream, sender_id, sender_name, body,
		       COALESCE(thread_id, '') AS thread_id, COALESCE(reply_to, '') AS reply_to, created_at
		FROM messages
		WHERE stream = ?
		   OR (sender_id = ? AND stream GLOB 'inbox:*' AND stream <> ?)`
	args := []any{inbox, self, inbox}

	var q string
	if limit > 0 {
		// Take the most recent `limit` rows, then re-sort ascending so the
		// client renders oldest-to-newest.
		q = `SELECT ` + cols + ` FROM (` + inner + `
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		) ORDER BY created_at ASC, id ASC`

		args = append(args, limit)
	} else {
		q = inner + `
		ORDER BY created_at ASC, id ASC`
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query conversation: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var msgs []Message

	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Seq, &m.Stream, &m.SenderID, &m.SenderName, &m.Body, &m.ThreadID, &m.ReplyTo, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation message: %w", err)
		}

		m.System = isSystemSender(m.SenderID)

		msgs = append(msgs, m)
	}

	return msgs, rows.Err()
}

func (s *MsgStore) Ack(stream, subscriber string, upToSeq int64) error {
	_, err := s.db.Exec(
		`INSERT INTO cursors (subscriber, stream, ack_seq, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(subscriber, stream) DO UPDATE SET
		   ack_seq = MAX(cursors.ack_seq, excluded.ack_seq),
		   updated_at = excluded.updated_at`,
		subscriber, stream, upToSeq, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("ack: %w", err)
	}

	return nil
}

func (s *MsgStore) AckMessages(stream, subscriber string, seqs []int64) error {
	if len(seqs) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("ack messages: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339Nano)

	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO acked_messages (subscriber, stream, seq, acked_at)
		 VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("ack messages: prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, seq := range seqs {
		if _, err := stmt.Exec(subscriber, stream, seq, now); err != nil {
			return fmt.Errorf("ack messages: insert seq %d: %w", seq, err)
		}
	}

	return tx.Commit()
}

func (s *MsgStore) AckLatest(stream, subscriber string) error {
	var maxSeq int64

	err := s.db.QueryRow(`
		SELECT MAX(s) FROM (
			SELECT COALESCE(MAX(seq), 0) AS s FROM messages WHERE stream = ?
			UNION ALL
			SELECT COALESCE(max_seq, 0) AS s FROM stream_hwm WHERE stream = ?
		)`, stream, stream).Scan(&maxSeq)
	if err != nil {
		return fmt.Errorf("ack latest: %w", err)
	}

	return s.Ack(stream, subscriber, maxSeq)
}

func (s *MsgStore) ListStreams(subscriber string, includeSystem bool) ([]StreamInfo, error) {
	q := `
		SELECT
			m.stream,
			COUNT(*) as total,
			COUNT(*) - COALESCE(
				(SELECT COUNT(*) FROM messages m2
				 WHERE m2.stream = m.stream
				   AND (m2.seq <= COALESCE(
				     (SELECT ack_seq FROM cursors WHERE subscriber = ? AND stream = m.stream), 0
				   ) OR m2.seq IN (SELECT seq FROM acked_messages WHERE subscriber = ? AND stream = m.stream))
				), 0
			) as unread,
			MAX(m.created_at) as latest_at
		FROM messages m`
	if !includeSystem {
		q += ` WHERE m.stream NOT LIKE '_system.%'`
	}

	q += `
		GROUP BY m.stream
		ORDER BY latest_at DESC`

	rows, err := s.db.Query(q, subscriber, subscriber)
	if err != nil {
		return nil, fmt.Errorf("list streams: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var streams []StreamInfo

	for rows.Next() {
		var si StreamInfo
		if err := rows.Scan(&si.Name, &si.Total, &si.Unread, &si.LatestAt); err != nil {
			return nil, fmt.Errorf("scan stream info: %w", err)
		}

		streams = append(streams, si)
	}

	return streams, rows.Err()
}

func (s *MsgStore) TotalUnread(subscriber string) int {
	var count int

	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM messages m
		WHERE m.stream = 'inbox:' || ?
		  AND m.seq > COALESCE(
			(SELECT c.ack_seq FROM cursors c
			 WHERE c.subscriber = ? AND c.stream = m.stream), 0
		  )
		  AND m.seq NOT IN (
			SELECT seq FROM acked_messages
			WHERE subscriber = ? AND stream = m.stream
		  )
	`, subscriber, subscriber, subscriber).Scan(&count)
	if err != nil {
		return 0
	}

	return count
}

func (s *MsgStore) Subscribe(stream string) (chan Message, func()) {
	buffer := s.subscriberBuffer
	if buffer < 1 {
		buffer = config.MessagesSubscriberBufferDefault
	}

	ch := make(chan Message, buffer)

	s.mu.Lock()
	s.subs[stream] = append(s.subs[stream], ch)
	s.mu.Unlock()

	unsub := func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		subs := s.subs[stream]
		for i, sub := range subs {
			if sub == ch {
				s.subs[stream] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
	}

	return ch, unsub
}

func (s *MsgStore) Cleanup(maxAge time.Duration, maxPerStream int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var (
		total     int64
		ageCutoff string
	)

	if maxAge > 0 {
		ageCutoff = time.Now().UTC().Add(-maxAge).Format(time.RFC3339Nano)

		res, err := s.db.Exec("DELETE FROM messages WHERE created_at < ?", ageCutoff)
		if err != nil {
			return 0, fmt.Errorf("cleanup by age: %w", err)
		}

		n, _ := res.RowsAffected()
		total += n

		// Jailed PR comments respect the same age retention (issue #1082) so a
		// quarantine store can't grow without bound. Keyed on jailed_at.
		jres, err := s.db.Exec("DELETE FROM jailed_comments WHERE jailed_at < ?", ageCutoff)
		if err != nil {
			return total, fmt.Errorf("cleanup jailed by age: %w", err)
		}

		jn, _ := jres.RowsAffected()
		total += jn
	}

	if maxPerStream > 0 {
		type streamCount struct {
			name  string
			count int64
		}

		// Collect the over-quota streams in a closure so the rows handle is
		// closed (via defer) before the DELETE queries below reuse the
		// connection.
		streams, err := func() ([]streamCount, error) {
			rows, err := s.db.Query("SELECT stream, COUNT(*) as cnt FROM messages GROUP BY stream HAVING cnt > ?", maxPerStream)
			if err != nil {
				return nil, fmt.Errorf("cleanup by count: list streams: %w", err)
			}
			defer func() { _ = rows.Close() }()

			var out []streamCount

			for rows.Next() {
				var sc streamCount
				if err := rows.Scan(&sc.name, &sc.count); err != nil {
					return nil, fmt.Errorf("cleanup by count: scan: %w", err)
				}

				out = append(out, sc)
			}

			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("cleanup by count: iterate streams: %w", err)
			}

			return out, nil
		}()
		if err != nil {
			return total, err
		}

		for _, sc := range streams {
			res, err := s.db.Exec(
				`DELETE FROM messages WHERE stream = ? AND id NOT IN (
					SELECT id FROM messages WHERE stream = ? ORDER BY seq DESC LIMIT ?
				)`,
				sc.name, sc.name, maxPerStream,
			)
			if err != nil {
				return total, fmt.Errorf("cleanup by count: delete from %s: %w", sc.name, err)
			}

			n, _ := res.RowsAffected()
			total += n
		}
	}

	_, _ = s.db.Exec(`DELETE FROM acked_messages WHERE NOT EXISTS (
		SELECT 1 FROM messages WHERE messages.stream = acked_messages.stream AND messages.seq = acked_messages.seq
	)`)

	_, _ = s.db.Exec(`DELETE FROM cursors WHERE NOT EXISTS (
		SELECT 1 FROM messages WHERE messages.stream = cursors.stream
	)`)

	if ageCutoff != "" {
		_, _ = s.db.Exec("DELETE FROM cursors WHERE updated_at < ?", ageCutoff)
	}

	return total, nil
}

func (s *MsgStore) Close() error {
	return s.db.Close()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}

	return s
}
