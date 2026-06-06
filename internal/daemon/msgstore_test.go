package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *MsgStore {
	t.Helper()
	dir := t.TempDir()
	s, err := NewMsgStore(filepath.Join(dir, "test.sqlite"))
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPublishAndRead(t *testing.T) {
	s := testStore(t)

	msg, err := s.Publish("test-topic", "sess1", "agent-a", "hello world", "", "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if msg.Seq != 1 {
		t.Errorf("seq = %d, want 1", msg.Seq)
	}
	if msg.Stream != "test-topic" {
		t.Errorf("stream = %q, want test-topic", msg.Stream)
	}

	msg2, err := s.Publish("test-topic", "sess2", "agent-b", "reply", "", "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if msg2.Seq != 2 {
		t.Errorf("seq = %d, want 2", msg2.Seq)
	}

	msgs, err := s.Read("test-topic", "", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Body != "hello world" {
		t.Errorf("msgs[0].Body = %q", msgs[0].Body)
	}
	if msgs[1].Body != "reply" {
		t.Errorf("msgs[1].Body = %q", msgs[1].Body)
	}
}

func TestReadUnackedOnly(t *testing.T) {
	s := testStore(t)

	s.Publish("topic", "s1", "a", "msg1", "", "")
	s.Publish("topic", "s1", "a", "msg2", "", "")

	if err := s.AckLatest("topic", "reader1"); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	s.Publish("topic", "s1", "a", "msg3", "", "")

	msgs, err := s.Read("topic", "reader1", true, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (only unread)", len(msgs))
	}
	if msgs[0].Body != "msg3" {
		t.Errorf("body = %q, want msg3", msgs[0].Body)
	}
}

func TestReadAllIgnoresCursor(t *testing.T) {
	s := testStore(t)

	s.Publish("topic", "s1", "a", "msg1", "", "")
	s.Publish("topic", "s1", "a", "msg2", "", "")
	s.AckLatest("topic", "reader1")

	msgs, err := s.Read("topic", "reader1", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (all)", len(msgs))
	}
}

func TestThreadFiltering(t *testing.T) {
	s := testStore(t)

	msg1, _ := s.Publish("topic", "s1", "a", "start thread", "", "")
	s.Publish("topic", "s1", "a", "unrelated", "", "")
	s.Publish("topic", "s2", "b", "reply in thread", msg1.ID, "")

	msgs, err := s.Read("topic", "", false, msg1.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (thread only)", len(msgs))
	}
	if msgs[0].Body != "reply in thread" {
		t.Errorf("body = %q", msgs[0].Body)
	}
}

func TestListStreams(t *testing.T) {
	s := testStore(t)

	s.Publish("alpha", "s1", "a", "m1", "", "")
	s.Publish("alpha", "s1", "a", "m2", "", "")
	s.Publish("beta", "s1", "a", "m3", "", "")
	s.AckLatest("alpha", "reader1")

	streams, err := s.ListStreams("reader1")
	if err != nil {
		t.Fatalf("ListStreams: %v", err)
	}
	if len(streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(streams))
	}

	byName := make(map[string]StreamInfo)
	for _, si := range streams {
		byName[si.Name] = si
	}

	alpha := byName["alpha"]
	if alpha.Total != 2 {
		t.Errorf("alpha.Total = %d, want 2", alpha.Total)
	}
	if alpha.Unread != 0 {
		t.Errorf("alpha.Unread = %d, want 0", alpha.Unread)
	}

	beta := byName["beta"]
	if beta.Total != 1 {
		t.Errorf("beta.Total = %d, want 1", beta.Total)
	}
	if beta.Unread != 1 {
		t.Errorf("beta.Unread = %d, want 1", beta.Unread)
	}
}

func TestSubscribeReceivesPublished(t *testing.T) {
	s := testStore(t)

	ch, unsub := s.Subscribe("events")
	defer unsub()

	s.Publish("events", "s1", "a", "event1", "", "")

	select {
	case msg := <-ch:
		if msg.Body != "event1" {
			t.Errorf("body = %q, want event1", msg.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscription message")
	}
}

func TestSubscribeDoesNotReceiveOtherStreams(t *testing.T) {
	s := testStore(t)

	ch, unsub := s.Subscribe("stream-a")
	defer unsub()

	s.Publish("stream-b", "s1", "a", "wrong stream", "", "")

	select {
	case msg := <-ch:
		t.Errorf("received unexpected message: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	s := testStore(t)

	ch, unsub := s.Subscribe("events")
	unsub()

	s.Publish("events", "s1", "a", "after unsub", "", "")

	select {
	case msg := <-ch:
		t.Errorf("received message after unsub: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSequencesArePerStream(t *testing.T) {
	s := testStore(t)

	m1, _ := s.Publish("stream-a", "s1", "a", "a1", "", "")
	m2, _ := s.Publish("stream-b", "s1", "a", "b1", "", "")
	m3, _ := s.Publish("stream-a", "s1", "a", "a2", "", "")

	if m1.Seq != 1 {
		t.Errorf("stream-a first msg seq = %d, want 1", m1.Seq)
	}
	if m2.Seq != 1 {
		t.Errorf("stream-b first msg seq = %d, want 1", m2.Seq)
	}
	if m3.Seq != 2 {
		t.Errorf("stream-a second msg seq = %d, want 2", m3.Seq)
	}
}

func TestReopenDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")

	s1, err := NewMsgStore(dbPath)
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}
	s1.Publish("topic", "s1", "a", "persisted", "", "")
	s1.Close()

	s2, err := NewMsgStore(dbPath)
	if err != nil {
		t.Fatalf("NewMsgStore (reopen): %v", err)
	}
	defer s2.Close()

	msgs, err := s2.Read("topic", "", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Body != "persisted" {
		t.Errorf("body = %q", msgs[0].Body)
	}

	m, _ := s2.Publish("topic", "s1", "a", "after reopen", "", "")
	if m.Seq != 2 {
		t.Errorf("seq after reopen = %d, want 2", m.Seq)
	}
}

func TestDBFileCreated(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "messages.sqlite")

	s, err := NewMsgStore(dbPath)
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}
	s.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file was not created")
	}
}

func TestAckSpecificSeq(t *testing.T) {
	s := testStore(t)

	s.Publish("topic", "s1", "a", "msg1", "", "")
	s.Publish("topic", "s1", "a", "msg2", "", "")
	s.Publish("topic", "s1", "a", "msg3", "", "")

	s.Ack("topic", "reader1", 2)

	msgs, err := s.Read("topic", "reader1", true, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Body != "msg3" {
		t.Errorf("body = %q, want msg3", msgs[0].Body)
	}
}

func TestAckDoesNotGoBackwards(t *testing.T) {
	s := testStore(t)

	s.Publish("topic", "s1", "a", "msg1", "", "")
	s.Publish("topic", "s1", "a", "msg2", "", "")
	s.Publish("topic", "s1", "a", "msg3", "", "")

	s.Ack("topic", "reader1", 3)
	s.Ack("topic", "reader1", 1)

	msgs, err := s.Read("topic", "reader1", true, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("got %d messages, want 0 (cursor should not go backwards)", len(msgs))
	}
}

func TestIndependentCursorsPerSubscriber(t *testing.T) {
	s := testStore(t)

	s.Publish("topic", "s1", "a", "msg1", "", "")
	s.Publish("topic", "s1", "a", "msg2", "", "")
	s.Publish("topic", "s1", "a", "msg3", "", "")

	s.Ack("topic", "reader-a", 2)
	s.Ack("topic", "reader-b", 1)

	msgsA, _ := s.Read("topic", "reader-a", true, "")
	msgsB, _ := s.Read("topic", "reader-b", true, "")

	if len(msgsA) != 1 {
		t.Errorf("reader-a got %d messages, want 1", len(msgsA))
	}
	if len(msgsB) != 2 {
		t.Errorf("reader-b got %d messages, want 2", len(msgsB))
	}
}

func TestReadEmptyStream(t *testing.T) {
	s := testStore(t)

	msgs, err := s.Read("nonexistent", "", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("got %d messages from empty stream, want 0", len(msgs))
	}
}

func TestReadEmptyStreamUnread(t *testing.T) {
	s := testStore(t)

	msgs, err := s.Read("nonexistent", "reader1", true, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("got %d messages, want 0", len(msgs))
	}
}

func TestPublishStoresAllFields(t *testing.T) {
	s := testStore(t)

	msg, err := s.Publish("topic", "sender-1", "Agent Alpha", "task body", "thread-42", "inbox:sender-1")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if msg.SenderID != "sender-1" {
		t.Errorf("SenderID = %q", msg.SenderID)
	}
	if msg.SenderName != "Agent Alpha" {
		t.Errorf("SenderName = %q", msg.SenderName)
	}
	if msg.ThreadID != "thread-42" {
		t.Errorf("ThreadID = %q", msg.ThreadID)
	}
	if msg.ReplyTo != "inbox:sender-1" {
		t.Errorf("ReplyTo = %q", msg.ReplyTo)
	}

	msgs, _ := s.Read("topic", "", false, "")
	m := msgs[0]
	if m.SenderID != "sender-1" || m.SenderName != "Agent Alpha" {
		t.Errorf("sender fields not persisted: %+v", m)
	}
	if m.ThreadID != "thread-42" || m.ReplyTo != "inbox:sender-1" {
		t.Errorf("thread/reply fields not persisted: %+v", m)
	}
	if m.CreatedAt == "" {
		t.Error("CreatedAt is empty")
	}
	if m.ID == "" || len(m.ID) < 5 {
		t.Errorf("ID looks wrong: %q", m.ID)
	}
}

func TestPublishEmptyThreadAndReplyStoreAsEmpty(t *testing.T) {
	s := testStore(t)

	s.Publish("topic", "s1", "a", "plain message", "", "")

	msgs, _ := s.Read("topic", "", false, "")
	if msgs[0].ThreadID != "" {
		t.Errorf("ThreadID = %q, want empty", msgs[0].ThreadID)
	}
	if msgs[0].ReplyTo != "" {
		t.Errorf("ReplyTo = %q, want empty", msgs[0].ReplyTo)
	}
}

func TestListStreamsEmpty(t *testing.T) {
	s := testStore(t)

	streams, err := s.ListStreams("anyone")
	if err != nil {
		t.Fatalf("ListStreams: %v", err)
	}
	if len(streams) != 0 {
		t.Errorf("got %d streams, want 0", len(streams))
	}
}

func TestListStreamsNoSubscriber(t *testing.T) {
	s := testStore(t)

	s.Publish("alpha", "s1", "a", "m1", "", "")
	s.Publish("beta", "s1", "a", "m2", "", "")

	streams, err := s.ListStreams("")
	if err != nil {
		t.Fatalf("ListStreams: %v", err)
	}
	if len(streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(streams))
	}
	for _, si := range streams {
		if si.Total != 1 {
			t.Errorf("%s: Total = %d, want 1", si.Name, si.Total)
		}
		if si.Unread != 1 {
			t.Errorf("%s: Unread = %d, want 1", si.Name, si.Unread)
		}
	}
}

func TestAckLatest(t *testing.T) {
	s := testStore(t)

	s.Publish("topic", "s1", "a", "msg1", "", "")
	s.Publish("topic", "s1", "a", "msg2", "", "")

	s.AckLatest("topic", "reader1")

	s.Publish("topic", "s1", "a", "msg3", "", "")

	msgs, _ := s.Read("topic", "reader1", true, "")
	if len(msgs) != 1 {
		t.Fatalf("got %d, want 1", len(msgs))
	}
	if msgs[0].Body != "msg3" {
		t.Errorf("body = %q", msgs[0].Body)
	}
}

func TestAckLatestEmptyStream(t *testing.T) {
	s := testStore(t)

	err := s.AckLatest("empty", "reader1")
	if err != nil {
		t.Fatalf("AckLatest on empty stream: %v", err)
	}
}

func TestConcurrentPublish(t *testing.T) {
	s := testStore(t)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			_, err := s.Publish("concurrent", "s1", "a", "msg", "", "")
			if err != nil {
				t.Errorf("publish %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	msgs, _ := s.Read("concurrent", "", false, "")
	if len(msgs) != n {
		t.Errorf("got %d messages, want %d", len(msgs), n)
	}

	seqs := make(map[int64]bool)
	for _, m := range msgs {
		if seqs[m.Seq] {
			t.Errorf("duplicate seq %d", m.Seq)
		}
		seqs[m.Seq] = true
	}
}

func TestMultipleSubscribersOnSameStream(t *testing.T) {
	s := testStore(t)

	ch1, unsub1 := s.Subscribe("events")
	defer unsub1()
	ch2, unsub2 := s.Subscribe("events")
	defer unsub2()

	s.Publish("events", "s1", "a", "broadcast", "", "")

	for _, ch := range []chan Message{ch1, ch2} {
		select {
		case msg := <-ch:
			if msg.Body != "broadcast" {
				t.Errorf("body = %q", msg.Body)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for broadcast message")
		}
	}
}

func TestMessageToJSON(t *testing.T) {
	s := testStore(t)

	msg, _ := s.Publish("topic", "s1", "agent-a", "hello", "thread-1", "inbox:s1")

	data := s.MessageToJSON(msg)
	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ID != msg.ID {
		t.Errorf("ID = %q, want %q", decoded.ID, msg.ID)
	}
	if decoded.Body != "hello" {
		t.Errorf("Body = %q", decoded.Body)
	}
	if decoded.ThreadID != "thread-1" {
		t.Errorf("ThreadID = %q", decoded.ThreadID)
	}
}

func TestCursorsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")

	s1, _ := NewMsgStore(dbPath)
	s1.Publish("topic", "s1", "a", "msg1", "", "")
	s1.Publish("topic", "s1", "a", "msg2", "", "")
	s1.Ack("topic", "reader1", 1)
	s1.Close()

	s2, _ := NewMsgStore(dbPath)
	defer s2.Close()

	s2.Publish("topic", "s1", "a", "msg3", "", "")

	msgs, _ := s2.Read("topic", "reader1", true, "")
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (seq 2 and 3)", len(msgs))
	}
	if msgs[0].Body != "msg2" {
		t.Errorf("first unread = %q, want msg2", msgs[0].Body)
	}
}

func TestThreadFilterWithUnreadCursor(t *testing.T) {
	s := testStore(t)

	m1, _ := s.Publish("topic", "s1", "a", "thread start", "", "")
	s.Publish("topic", "s1", "a", "unrelated 1", "", "")
	s.Publish("topic", "s2", "b", "thread reply", m1.ID, "")
	s.Publish("topic", "s1", "a", "unrelated 2", "", "")

	s.Ack("topic", "reader1", 2)

	msgs, _ := s.Read("topic", "reader1", true, m1.ID)
	if len(msgs) != 1 {
		t.Fatalf("got %d, want 1 (only unread in thread)", len(msgs))
	}
	if msgs[0].Body != "thread reply" {
		t.Errorf("body = %q", msgs[0].Body)
	}
}
