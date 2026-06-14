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
}

func NewMsgStore(dbPath string) (*MsgStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("create messages db dir: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open messages db: %w", err)
	}

	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	return &MsgStore{
		db:   db,
		subs: make(map[string][]chan Message),
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

func (s *MsgStore) Publish(stream, senderID, senderName, body, threadID, replyTo string) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

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
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Seq, &m.Stream, &m.SenderID, &m.SenderName, &m.Body, &m.ThreadID, &m.ReplyTo, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
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
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO acked_messages (subscriber, stream, seq, acked_at)
		 VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("ack messages: prepare: %w", err)
	}
	defer stmt.Close()

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
	defer rows.Close()

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
	ch := make(chan Message, 64)

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

	var total int64
	var ageCutoff string

	if maxAge > 0 {
		ageCutoff = time.Now().UTC().Add(-maxAge).Format(time.RFC3339Nano)
		res, err := s.db.Exec("DELETE FROM messages WHERE created_at < ?", ageCutoff)
		if err != nil {
			return 0, fmt.Errorf("cleanup by age: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
	}

	if maxPerStream > 0 {
		rows, err := s.db.Query("SELECT stream, COUNT(*) as cnt FROM messages GROUP BY stream HAVING cnt > ?", maxPerStream)
		if err != nil {
			return total, fmt.Errorf("cleanup by count: list streams: %w", err)
		}

		type streamCount struct {
			name  string
			count int64
		}
		var streams []streamCount
		for rows.Next() {
			var sc streamCount
			if err := rows.Scan(&sc.name, &sc.count); err != nil {
				rows.Close()
				return total, fmt.Errorf("cleanup by count: scan: %w", err)
			}
			streams = append(streams, sc)
		}
		rows.Close()

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

	s.db.Exec(`DELETE FROM acked_messages WHERE NOT EXISTS (
		SELECT 1 FROM messages WHERE messages.stream = acked_messages.stream AND messages.seq = acked_messages.seq
	)`)

	s.db.Exec(`DELETE FROM cursors WHERE NOT EXISTS (
		SELECT 1 FROM messages WHERE messages.stream = cursors.stream
	)`)

	if ageCutoff != "" {
		s.db.Exec("DELETE FROM cursors WHERE updated_at < ?", ageCutoff)
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
