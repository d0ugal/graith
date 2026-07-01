package daemon

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestServerAcceptsConnections(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	l, err := Listen(sockPath)
	if err != nil {
		t.Fatal(err)
	}

	var count atomic.Int32

	handler := func(ctx context.Context, conn net.Conn) {
		count.Add(1)

		buf := make([]byte, 16)
		conn.Read(buf)
	}

	srv := NewServer(l, handler, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Serve(ctx)

	conn1, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}

	conn1.Write([]byte("hi"))
	conn1.Close()

	conn2, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}

	conn2.Write([]byte("hi"))
	conn2.Close()

	time.Sleep(100 * time.Millisecond)
	srv.Shutdown()

	if count.Load() != 2 {
		t.Errorf("handled %d connections, want 2", count.Load())
	}
}
