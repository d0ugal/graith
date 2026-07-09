package daemon

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) *MsgStore {
	t.Helper()
	dir := t.TempDir()

	s, err := NewMsgStore(filepath.Join(dir, "test.sqlite"))
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	return s
}

func TestPublishAndRead(t *testing.T) {
	s := testStore(t)

	msg, err := s.Publish("blether-topic", "braw-sess", "bonnie-a", "braw day", "", "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if msg.Seq != 1 {
		t.Errorf("seq = %d, want 1", msg.Seq)
	}

	if msg.Stream != "blether-topic" {
		t.Errorf("stream = %q, want blether-topic", msg.Stream)
	}

	msg2, err := s.Publish("blether-topic", "canny-sess", "bonnie-b", "bonnie reply", "", "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if msg2.Seq != 2 {
		t.Errorf("seq = %d, want 2", msg2.Seq)
	}

	msgs, err := s.Read("blether-topic", "", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}

	if msgs[0].Body != "braw day" {
		t.Errorf("msgs[0].Body = %q", msgs[0].Body)
	}

	if msgs[1].Body != "bonnie reply" {
		t.Errorf("msgs[1].Body = %q", msgs[1].Body)
	}
}

func TestSystemMarkerDerivedFromSender(t *testing.T) {
	s := testStore(t)

	// A daemon-authored notification is flagged system; an ordinary
	// session-authored message is not. See issue #887.
	sysMsg, err := s.Publish("inbox:braw-sess", systemSenderID, systemSenderName, "PR merged", "", "")
	if err != nil {
		t.Fatalf("Publish system: %v", err)
	}

	if !sysMsg.System {
		t.Errorf("system notification: System = false, want true")
	}

	sessMsg, err := s.Publish("inbox:braw-sess", "canny-sess", "canny", "hullo", "", "")
	if err != nil {
		t.Fatalf("Publish session: %v", err)
	}

	if sessMsg.System {
		t.Errorf("session message: System = true, want false")
	}

	// The marker is derived on read too, not only on publish.
	msgs, err := s.Read("inbox:braw-sess", "", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}

	if !msgs[0].System {
		t.Errorf("read msgs[0].System = false, want true (system notification)")
	}

	if msgs[1].System {
		t.Errorf("read msgs[1].System = true, want false (session message)")
	}
}

func TestReadUnackedOnly(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("blether", "braw1", "neep", "neep1", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep2", "", "")

	if err := s.AckLatest("blether", "kirk-reader1"); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	_, _ = s.Publish("blether", "braw1", "neep", "neep3", "", "")

	msgs, err := s.Read("blether", "kirk-reader1", true, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (only unread)", len(msgs))
	}

	if msgs[0].Body != "neep3" {
		t.Errorf("body = %q, want neep3", msgs[0].Body)
	}
}

func TestReadAllIgnoresCursor(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("blether", "braw1", "neep", "neep1", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep2", "", "")
	_ = s.AckLatest("blether", "kirk-reader1")

	msgs, err := s.Read("blether", "kirk-reader1", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (all)", len(msgs))
	}
}

func TestThreadFiltering(t *testing.T) {
	s := testStore(t)

	msg1, _ := s.Publish("blether", "braw1", "neep", "kirk-start", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "thrawn", "", "")
	_, _ = s.Publish("blether", "canny1", "whin", "kirk-reply", msg1.ID, "")

	msgs, err := s.Read("blether", "", false, msg1.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (thread only)", len(msgs))
	}

	if msgs[0].Body != "kirk-reply" {
		t.Errorf("body = %q", msgs[0].Body)
	}
}

func TestListStreams(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("braw-stream", "braw1", "neep", "wee-neep1", "", "")
	_, _ = s.Publish("braw-stream", "braw1", "neep", "wee-neep2", "", "")
	_, _ = s.Publish("canny-stream", "braw1", "neep", "wee-neep3", "", "")
	_ = s.AckLatest("braw-stream", "kirk-reader1")

	streams, err := s.ListStreams("kirk-reader1", true)
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

	alpha := byName["braw-stream"]
	if alpha.Total != 2 {
		t.Errorf("braw-stream.Total = %d, want 2", alpha.Total)
	}

	if alpha.Unread != 0 {
		t.Errorf("braw-stream.Unread = %d, want 0", alpha.Unread)
	}

	beta := byName["canny-stream"]
	if beta.Total != 1 {
		t.Errorf("canny-stream.Total = %d, want 1", beta.Total)
	}

	if beta.Unread != 1 {
		t.Errorf("canny-stream.Unread = %d, want 1", beta.Unread)
	}
}

func TestSubscribeReceivesPublished(t *testing.T) {
	s := testStore(t)

	ch, unsub := s.Subscribe("kirk-events")
	defer unsub()

	_, _ = s.Publish("kirk-events", "braw1", "neep", "kirk-event1", "", "")

	select {
	case msg := <-ch:
		if msg.Body != "kirk-event1" {
			t.Errorf("body = %q, want kirk-event1", msg.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscription message")
	}
}

func TestSubscribeDoesNotReceiveOtherStreams(t *testing.T) {
	s := testStore(t)

	ch, unsub := s.Subscribe("blether-braw")
	defer unsub()

	_, _ = s.Publish("blether-canny", "braw1", "neep", "thrawn stream", "", "")

	select {
	case msg := <-ch:
		t.Errorf("received unexpected message: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	s := testStore(t)

	ch, unsub := s.Subscribe("kirk-events")
	unsub()

	_, _ = s.Publish("kirk-events", "braw1", "neep", "efter unsub", "", "")

	select {
	case msg := <-ch:
		t.Errorf("received message after unsub: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSequencesArePerStream(t *testing.T) {
	s := testStore(t)

	m1, _ := s.Publish("blether-braw", "braw1", "neep", "braw-a1", "", "")
	m2, _ := s.Publish("blether-canny", "braw1", "neep", "canny-b1", "", "")
	m3, _ := s.Publish("blether-braw", "braw1", "neep", "braw-a2", "", "")

	if m1.Seq != 1 {
		t.Errorf("blether-braw first msg seq = %d, want 1", m1.Seq)
	}

	if m2.Seq != 1 {
		t.Errorf("blether-canny first msg seq = %d, want 1", m2.Seq)
	}

	if m3.Seq != 2 {
		t.Errorf("blether-braw second msg seq = %d, want 2", m3.Seq)
	}
}

func TestReopenDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")

	s1, err := NewMsgStore(dbPath)
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}

	_, _ = s1.Publish("blether", "braw1", "neep", "bide-msg", "", "")
	_ = s1.Close()

	s2, err := NewMsgStore(dbPath)
	if err != nil {
		t.Fatalf("NewMsgStore (reopen): %v", err)
	}
	defer func() { _ = s2.Close() }()

	msgs, err := s2.Read("blether", "", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}

	if msgs[0].Body != "bide-msg" {
		t.Errorf("body = %q", msgs[0].Body)
	}

	m, _ := s2.Publish("blether", "braw1", "neep", "efter reopen", "", "")
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

	_ = s.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file was not created")
	}
}

func TestAckSpecificSeq(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("blether", "braw1", "neep", "neep1", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep2", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep3", "", "")

	_ = s.Ack("blether", "kirk-reader1", 2)

	msgs, err := s.Read("blether", "kirk-reader1", true, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}

	if msgs[0].Body != "neep3" {
		t.Errorf("body = %q, want neep3", msgs[0].Body)
	}
}

func TestAckDoesNotGoBackwards(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("blether", "braw1", "neep", "neep1", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep2", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep3", "", "")

	_ = s.Ack("blether", "kirk-reader1", 3)
	_ = s.Ack("blether", "kirk-reader1", 1)

	msgs, err := s.Read("blether", "kirk-reader1", true, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 0 {
		t.Fatalf("got %d messages, want 0 (cursor should not go backwards)", len(msgs))
	}
}

func TestIndependentCursorsPerSubscriber(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("blether", "braw1", "neep", "neep1", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep2", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep3", "", "")

	_ = s.Ack("blether", "kirk-reader-a", 2)
	_ = s.Ack("blether", "kirk-reader-b", 1)

	msgsA, _ := s.Read("blether", "kirk-reader-a", true, "")
	msgsB, _ := s.Read("blether", "kirk-reader-b", true, "")

	if len(msgsA) != 1 {
		t.Errorf("kirk-reader-a got %d messages, want 1", len(msgsA))
	}

	if len(msgsB) != 2 {
		t.Errorf("kirk-reader-b got %d messages, want 2", len(msgsB))
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

	msgs, err := s.Read("nonexistent", "kirk-reader1", true, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 0 {
		t.Errorf("got %d messages, want 0", len(msgs))
	}
}

func TestPublishStoresAllFields(t *testing.T) {
	s := testStore(t)

	msg, err := s.Publish("blether", "braw-sender", "Bonnie Alpha", "kirk-task", "kirk-42", "inbox:braw-sender")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if msg.SenderID != "braw-sender" {
		t.Errorf("SenderID = %q", msg.SenderID)
	}

	if msg.SenderName != "Bonnie Alpha" {
		t.Errorf("SenderName = %q", msg.SenderName)
	}

	if msg.ThreadID != "kirk-42" {
		t.Errorf("ThreadID = %q", msg.ThreadID)
	}

	if msg.ReplyTo != "inbox:braw-sender" {
		t.Errorf("ReplyTo = %q", msg.ReplyTo)
	}

	msgs, _ := s.Read("blether", "", false, "")

	m := msgs[0]
	if m.SenderID != "braw-sender" || m.SenderName != "Bonnie Alpha" {
		t.Errorf("sender fields not persisted: %+v", m)
	}

	if m.ThreadID != "kirk-42" || m.ReplyTo != "inbox:braw-sender" {
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

	_, _ = s.Publish("blether", "braw1", "neep", "neep message", "", "")

	msgs, _ := s.Read("blether", "", false, "")
	if msgs[0].ThreadID != "" {
		t.Errorf("ThreadID = %q, want empty", msgs[0].ThreadID)
	}

	if msgs[0].ReplyTo != "" {
		t.Errorf("ReplyTo = %q, want empty", msgs[0].ReplyTo)
	}
}

func TestListStreamsEmpty(t *testing.T) {
	s := testStore(t)

	streams, err := s.ListStreams("ony-body", true)
	if err != nil {
		t.Fatalf("ListStreams: %v", err)
	}

	if len(streams) != 0 {
		t.Errorf("got %d streams, want 0", len(streams))
	}
}

func TestListStreamsExcludesSystem(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("braw-stream", "braw1", "neep", "wee-neep1", "", "")
	_, _ = s.Publish("_system.status", "braw1", "neep", "braw change", "", "")

	streams, err := s.ListStreams("", false)
	if err != nil {
		t.Fatalf("ListStreams: %v", err)
	}

	if len(streams) != 1 {
		t.Fatalf("got %d streams, want 1 (system excluded)", len(streams))
	}

	if streams[0].Name != "braw-stream" {
		t.Errorf("got stream %q, want braw-stream", streams[0].Name)
	}

	streams, err = s.ListStreams("", true)
	if err != nil {
		t.Fatalf("ListStreams with system: %v", err)
	}

	if len(streams) != 2 {
		t.Fatalf("got %d streams, want 2 (system included)", len(streams))
	}
}

func TestTotalUnreadCountsOnlyInbox(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("inbox:braw-sess", "whin", "whin", "braw message", "", "")
	_, _ = s.Publish("inbox:braw-sess", "whin", "whin", "bonnie message", "", "")
	_, _ = s.Publish("braw-stream", "braw1", "neep", "braw-cast", "", "")
	_, _ = s.Publish("_system.status", "braw1", "neep", "braw change", "", "")
	_, _ = s.Publish("inbox:canny-sess", "whin", "whin", "whin inbox", "", "")

	count := s.TotalUnread("braw-sess")
	if count != 2 {
		t.Errorf("TotalUnread(braw-sess) = %d, want 2 (only inbox:braw-sess)", count)
	}

	count = s.TotalUnread("canny-sess")
	if count != 1 {
		t.Errorf("TotalUnread(canny-sess) = %d, want 1 (only inbox:canny-sess)", count)
	}

	count = s.TotalUnread("nae-body")
	if count != 0 {
		t.Errorf("TotalUnread(nae-body) = %d, want 0 (no inbox)", count)
	}
}

func TestListStreamsNoSubscriber(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("braw-stream", "braw1", "neep", "wee-neep1", "", "")
	_, _ = s.Publish("canny-stream", "braw1", "neep", "wee-neep2", "", "")

	streams, err := s.ListStreams("", true)
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

	_, _ = s.Publish("blether", "braw1", "neep", "neep1", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep2", "", "")

	_ = s.AckLatest("blether", "kirk-reader1")

	_, _ = s.Publish("blether", "braw1", "neep", "neep3", "", "")

	msgs, _ := s.Read("blether", "kirk-reader1", true, "")
	if len(msgs) != 1 {
		t.Fatalf("got %d, want 1", len(msgs))
	}

	if msgs[0].Body != "neep3" {
		t.Errorf("body = %q", msgs[0].Body)
	}
}

func TestAckLatestEmptyStream(t *testing.T) {
	s := testStore(t)

	err := s.AckLatest("empty", "kirk-reader1")
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

			_, err := s.Publish("braw-concurrent", "braw1", "neep", "neep", "", "")
			if err != nil {
				t.Errorf("publish %d: %v", i, err)
			}
		}(i)
	}

	wg.Wait()

	msgs, _ := s.Read("braw-concurrent", "", false, "")
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

	ch1, unsub1 := s.Subscribe("kirk-events")
	defer unsub1()

	ch2, unsub2 := s.Subscribe("kirk-events")
	defer unsub2()

	_, _ = s.Publish("kirk-events", "braw1", "neep", "braw-cast", "", "")

	for _, ch := range []chan Message{ch1, ch2} {
		select {
		case msg := <-ch:
			if msg.Body != "braw-cast" {
				t.Errorf("body = %q", msg.Body)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for broadcast message")
		}
	}
}

func TestCursorsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")

	s1, _ := NewMsgStore(dbPath)
	_, _ = s1.Publish("blether", "braw1", "neep", "neep1", "", "")
	_, _ = s1.Publish("blether", "braw1", "neep", "neep2", "", "")
	_ = s1.Ack("blether", "kirk-reader1", 1)
	_ = s1.Close()

	s2, _ := NewMsgStore(dbPath)
	defer func() { _ = s2.Close() }()

	_, _ = s2.Publish("blether", "braw1", "neep", "neep3", "", "")

	msgs, _ := s2.Read("blether", "kirk-reader1", true, "")
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (seq 2 and 3)", len(msgs))
	}

	if msgs[0].Body != "neep2" {
		t.Errorf("first unread = %q, want neep2", msgs[0].Body)
	}
}

func TestThreadFilterWithUnreadCursor(t *testing.T) {
	s := testStore(t)

	m1, _ := s.Publish("blether", "braw1", "neep", "kirk-begin", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "thrawn-1", "", "")
	_, _ = s.Publish("blether", "canny1", "whin", "kirk-reply", m1.ID, "")
	_, _ = s.Publish("blether", "braw1", "neep", "thrawn-2", "", "")

	_ = s.Ack("blether", "kirk-reader1", 2)

	msgs, _ := s.Read("blether", "kirk-reader1", true, m1.ID)
	if len(msgs) != 1 {
		t.Fatalf("got %d, want 1 (only unread in thread)", len(msgs))
	}

	if msgs[0].Body != "kirk-reply" {
		t.Errorf("body = %q", msgs[0].Body)
	}
}

func TestAckMessagesDoesNotSkipOtherThreads(t *testing.T) {
	s := testStore(t)

	m1, _ := s.Publish("blether", "braw1", "neep", "kirk-a-start", "", "")
	_, _ = s.Publish("blether", "canny1", "whin", "kirk-b-msg", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "kirk-a-reply", m1.ID, "")
	_, _ = s.Publish("blether", "canny1", "whin", "haar-msg", "", "")

	// Thread filter returns only the reply (seq 3); the root message has no
	// thread_id set. Ack only the reply using per-message ack.
	threadMsgs, _ := s.Read("blether", "kirk-reader1", true, m1.ID)
	if len(threadMsgs) != 1 {
		t.Fatalf("thread msgs = %d, want 1", len(threadMsgs))
	}

	seqs := make([]int64, len(threadMsgs))
	for i, m := range threadMsgs {
		seqs[i] = m.Seq
	}

	_ = s.AckMessages("blether", "kirk-reader1", seqs)

	// All other messages (root, threadB, unthreaded) must remain unread.
	unread, _ := s.Read("blether", "kirk-reader1", true, "")
	if len(unread) != 3 {
		t.Fatalf("unread = %d, want 3 (root + threadB + unthreaded)", len(unread))
	}

	// Reading unread for thread-A should return nothing (already acked).
	threadUnread, _ := s.Read("blether", "kirk-reader1", true, m1.ID)
	if len(threadUnread) != 0 {
		t.Fatalf("thread unread = %d, want 0", len(threadUnread))
	}
}

func TestAckMessagesEmpty(t *testing.T) {
	s := testStore(t)

	err := s.AckMessages("blether", "kirk-reader1", nil)
	if err != nil {
		t.Fatalf("AckMessages(nil): %v", err)
	}

	err = s.AckMessages("blether", "kirk-reader1", []int64{})
	if err != nil {
		t.Fatalf("AckMessages([]): %v", err)
	}
}

func TestAckMessagesCombinesWithCursor(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("blether", "braw1", "neep", "neep1", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep2", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep3", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep4", "", "")

	// Advance cursor past seq 1.
	_ = s.Ack("blether", "kirk-reader1", 1)
	// Individually ack seq 3.
	_ = s.AckMessages("blether", "kirk-reader1", []int64{3})

	unread, _ := s.Read("blether", "kirk-reader1", true, "")
	if len(unread) != 2 {
		t.Fatalf("unread = %d, want 2 (neep2 and neep4)", len(unread))
	}

	if unread[0].Body != "neep2" {
		t.Errorf("unread[0] = %q, want neep2", unread[0].Body)
	}

	if unread[1].Body != "neep4" {
		t.Errorf("unread[1] = %q, want neep4", unread[1].Body)
	}
}

func TestAckMessagesIdempotent(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("blether", "braw1", "neep", "neep1", "", "")

	// Acking the same seq twice should not error.
	if err := s.AckMessages("blether", "kirk-reader1", []int64{1}); err != nil {
		t.Fatalf("first ack: %v", err)
	}

	if err := s.AckMessages("blether", "kirk-reader1", []int64{1}); err != nil {
		t.Fatalf("second ack: %v", err)
	}

	unread, _ := s.Read("blether", "kirk-reader1", true, "")
	if len(unread) != 0 {
		t.Fatalf("unread = %d, want 0", len(unread))
	}
}

func TestCleanupByAge(t *testing.T) {
	s := testStore(t)

	// Insert messages with backdated timestamps directly
	oldTime := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	newTime := time.Now().UTC().Format(time.RFC3339Nano)

	_, _ = s.db.Exec(
		`INSERT INTO messages (id, seq, stream, sender_id, sender_name, body, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"msg_old1", 1, "blether", "braw1", "neep", "auld neep 1", oldTime,
	)
	_, _ = s.db.Exec(
		`INSERT INTO messages (id, seq, stream, sender_id, sender_name, body, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"msg_old2", 2, "blether", "braw1", "neep", "auld neep 2", oldTime,
	)
	_, _ = s.db.Exec(
		`INSERT INTO messages (id, seq, stream, sender_id, sender_name, body, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"msg_new1", 3, "blether", "braw1", "neep", "braw neep", newTime,
	)

	deleted, err := s.Cleanup(24*time.Hour, 0)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}

	msgs, _ := s.Read("blether", "", false, "")
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}

	if msgs[0].Body != "braw neep" {
		t.Errorf("body = %q, want 'braw neep'", msgs[0].Body)
	}
}

func TestCleanupByMaxPerStream(t *testing.T) {
	s := testStore(t)

	for i := range 5 {
		_, _ = s.Publish("blether-braw", "braw1", "neep", fmt.Sprintf("a-msg-%d", i+1), "", "")
	}

	for i := range 3 {
		_, _ = s.Publish("blether-canny", "braw1", "neep", fmt.Sprintf("b-msg-%d", i+1), "", "")
	}

	deleted, err := s.Cleanup(0, 3)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}

	msgsA, _ := s.Read("blether-braw", "", false, "")
	if len(msgsA) != 3 {
		t.Fatalf("blether-braw: got %d messages, want 3", len(msgsA))
	}

	if msgsA[0].Body != "a-msg-3" {
		t.Errorf("blether-braw first remaining = %q, want a-msg-3", msgsA[0].Body)
	}

	msgsB, _ := s.Read("blether-canny", "", false, "")
	if len(msgsB) != 3 {
		t.Fatalf("blether-canny: got %d messages, want 3", len(msgsB))
	}
}

func TestCleanupBothPolicies(t *testing.T) {
	s := testStore(t)

	oldTime := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)

	// 2 old messages + 3 new messages = 5 total in stream
	_, _ = s.db.Exec(
		`INSERT INTO messages (id, seq, stream, sender_id, sender_name, body, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"msg_old1", 1, "blether", "braw1", "neep", "old1", oldTime,
	)
	_, _ = s.db.Exec(
		`INSERT INTO messages (id, seq, stream, sender_id, sender_name, body, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"msg_old2", 2, "blether", "braw1", "neep", "old2", oldTime,
	)

	for i := range 3 {
		_, _ = s.Publish("blether", "braw1", "neep", fmt.Sprintf("new%d", i+1), "", "")
	}

	deleted, err := s.Cleanup(24*time.Hour, 2)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if deleted < 2 {
		t.Errorf("deleted = %d, want at least 2", deleted)
	}

	msgs, _ := s.Read("blether", "", false, "")
	if len(msgs) > 2 {
		t.Errorf("got %d messages, want at most 2", len(msgs))
	}
}

func TestCleanupNoConfig(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("blether", "braw1", "neep", "neep1", "", "")

	deleted, err := s.Cleanup(0, 0)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}

	msgs, _ := s.Read("blether", "", false, "")
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
}

func TestCleanupPreservesHighWaterMark(t *testing.T) {
	s := testStore(t)

	_, _ = s.Publish("blether", "braw1", "neep", "neep1", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep2", "", "")
	_, _ = s.Publish("blether", "braw1", "neep", "neep3", "", "")

	_ = s.Ack("blether", "kirk-reader1", 3)

	oldTime := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	_, _ = s.db.Exec("UPDATE messages SET created_at = ? WHERE stream = ?", oldTime, "blether")

	deleted, err := s.Cleanup(24*time.Hour, 0)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if deleted != 3 {
		t.Errorf("deleted = %d, want 3", deleted)
	}

	msgs, _ := s.Read("blether", "", false, "")
	if len(msgs) != 0 {
		t.Fatalf("got %d messages, want 0 (all cleaned up)", len(msgs))
	}

	msg4, err := s.Publish("blether", "braw1", "neep", "neep4", "", "")
	if err != nil {
		t.Fatalf("Publish after cleanup: %v", err)
	}

	if msg4.Seq <= 3 {
		t.Errorf("seq after cleanup = %d, want > 3 (must continue past high-water mark)", msg4.Seq)
	}

	unread, _ := s.Read("blether", "kirk-reader1", true, "")
	if len(unread) != 1 {
		t.Fatalf("got %d unread, want 1 (subscriber must see post-cleanup message)", len(unread))
	}

	if unread[0].Body != "neep4" {
		t.Errorf("body = %q, want neep4", unread[0].Body)
	}
}

func TestCleanupByMaxPreservesHighWaterMark(t *testing.T) {
	s := testStore(t)

	for i := range 10 {
		_, _ = s.Publish("blether", "braw1", "neep", fmt.Sprintf("msg-%d", i+1), "", "")
	}

	_ = s.Ack("blether", "kirk-reader1", 10)

	deleted, err := s.Cleanup(0, 3)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if deleted != 7 {
		t.Errorf("deleted = %d, want 7", deleted)
	}

	msg, err := s.Publish("blether", "braw1", "neep", "after-cleanup", "", "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if msg.Seq != 11 {
		t.Errorf("seq = %d, want 11", msg.Seq)
	}

	unread, _ := s.Read("blether", "kirk-reader1", true, "")
	if len(unread) != 1 {
		t.Fatalf("got %d unread, want 1", len(unread))
	}

	if unread[0].Body != "after-cleanup" {
		t.Errorf("body = %q", unread[0].Body)
	}
}

func TestCleanupAfterUpgradePreservesHWM(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE messages (
			id TEXT PRIMARY KEY, seq INTEGER NOT NULL, stream TEXT NOT NULL,
			sender_id TEXT NOT NULL, sender_name TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL, thread_id TEXT, reply_to TEXT, created_at TEXT NOT NULL
		);
		CREATE INDEX idx_messages_stream_seq ON messages(stream, seq);
		CREATE INDEX idx_messages_created_at ON messages(created_at);
		CREATE TABLE cursors (
			subscriber TEXT NOT NULL, stream TEXT NOT NULL,
			ack_seq INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL,
			PRIMARY KEY (subscriber, stream)
		);
	`)
	if err != nil {
		t.Fatalf("create pre-upgrade schema: %v", err)
	}

	oldTime := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	for i := 1; i <= 5; i++ {
		_, _ = db.Exec(
			`INSERT INTO messages (id, seq, stream, sender_id, sender_name, body, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("msg_%d", i), i, "blether", "braw1", "neep", fmt.Sprintf("old-%d", i), oldTime,
		)
	}

	_, _ = db.Exec(
		`INSERT INTO cursors (subscriber, stream, ack_seq, updated_at) VALUES (?, ?, ?, ?)`,
		"kirk-reader1", "blether", 5, oldTime,
	)
	_ = db.Close()

	s, err := NewMsgStore(dbPath)
	if err != nil {
		t.Fatalf("NewMsgStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	deleted, err := s.Cleanup(24*time.Hour, 0)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if deleted != 5 {
		t.Errorf("deleted = %d, want 5", deleted)
	}

	msg, err := s.Publish("blether", "braw1", "neep", "post-upgrade", "", "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if msg.Seq <= 5 {
		t.Errorf("seq = %d, want > 5 (must continue past pre-upgrade high-water mark)", msg.Seq)
	}

	unread, _ := s.Read("blether", "kirk-reader1", true, "")
	if len(unread) != 1 {
		t.Fatalf("got %d unread, want 1", len(unread))
	}

	if unread[0].Body != "post-upgrade" {
		t.Errorf("body = %q, want post-upgrade", unread[0].Body)
	}
}

func TestCleanupEmptyDB(t *testing.T) {
	s := testStore(t)

	deleted, err := s.Cleanup(24*time.Hour, 100)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
}

func TestCleanupRemovesStaleCursors(t *testing.T) {
	s := testStore(t)

	oldTime := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	newTime := time.Now().UTC().Format(time.RFC3339Nano)

	// Publish a fresh message on the stream so it is NOT orphaned — this
	// isolates the stale-cursor path from the orphan-cursor path.
	_, _ = s.Publish("bonnie-stream", "braw1", "neep", "bonnie-recent", "", "")

	// Insert a stale cursor on the same stream (old updated_at).
	_, _ = s.db.Exec(
		`INSERT INTO cursors (subscriber, stream, ack_seq, updated_at)
		 VALUES (?, ?, ?, ?)`,
		"auld-reader", "bonnie-stream", 1, oldTime,
	)

	// Insert an active cursor on the same stream (recent updated_at).
	_, _ = s.db.Exec(
		`INSERT INTO cursors (subscriber, stream, ack_seq, updated_at)
		 VALUES (?, ?, ?, ?)`,
		"braw-reader", "bonnie-stream", 1, newTime,
	)

	// Cleanup with a 24h max age — the stream still has messages so only
	// the age-based cursor check should remove the stale cursor.
	_, err := s.Cleanup(24*time.Hour, 0)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// The stale cursor should be gone (updated_at is older than the cutoff).
	var staleCursorCount int

	_ = s.db.QueryRow("SELECT COUNT(*) FROM cursors WHERE subscriber = ?", "auld-reader").Scan(&staleCursorCount)

	if staleCursorCount != 0 {
		t.Errorf("stale cursor count = %d, want 0", staleCursorCount)
	}

	// The active cursor should remain.
	var activeCursorCount int

	_ = s.db.QueryRow("SELECT COUNT(*) FROM cursors WHERE subscriber = ?", "braw-reader").Scan(&activeCursorCount)

	if activeCursorCount != 1 {
		t.Errorf("active cursor count = %d, want 1", activeCursorCount)
	}
}

func TestCleanupRemovesOrphanedCursors(t *testing.T) {
	s := testStore(t)

	// Create messages on two streams.
	_, _ = s.Publish("keep-stream", "s1", "a", "keep this", "", "")
	_, _ = s.Publish("remove-stream", "s1", "a", "remove this", "", "")

	// Create cursors for both streams.
	_ = s.Ack("keep-stream", "reader1", 1)
	_ = s.Ack("remove-stream", "reader1", 1)

	// Delete all messages from remove-stream manually (simulating age-based cleanup).
	_, _ = s.db.Exec("DELETE FROM messages WHERE stream = ?", "remove-stream")

	// Run cleanup — the orphaned cursor for remove-stream should be removed.
	_, _ = s.Cleanup(0, 0)

	var orphanedCount int

	_ = s.db.QueryRow("SELECT COUNT(*) FROM cursors WHERE stream = ?", "remove-stream").Scan(&orphanedCount)

	if orphanedCount != 0 {
		t.Errorf("orphaned cursor count = %d, want 0", orphanedCount)
	}

	var keptCount int

	_ = s.db.QueryRow("SELECT COUNT(*) FROM cursors WHERE stream = ?", "keep-stream").Scan(&keptCount)

	if keptCount != 1 {
		t.Errorf("kept cursor count = %d, want 1", keptCount)
	}
}
