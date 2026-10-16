//go:build darwin

package daemonservice

import (
	"context"
	"errors"
	"testing"
	"time"
)

func stubMacOSVersion(t *testing.T, fn func(context.Context) ([]byte, error)) {
	t.Helper()

	original := readMacOSVersion

	macOSMajorCache.Lock()
	macOSMajorCache.major = 0
	macOSMajorCache.Unlock()

	readMacOSVersion = fn

	t.Cleanup(func() {
		readMacOSVersion = original

		macOSMajorCache.Lock()
		macOSMajorCache.major = 0
		macOSMajorCache.Unlock()
	})
}

func TestCurrentMacOSMajorCachesSuccess(t *testing.T) {
	calls := 0

	stubMacOSVersion(t, func(context.Context) ([]byte, error) {
		calls++

		return []byte("15.4.1\n"), nil
	})

	for range 3 {
		major, err := currentMacOSMajor()
		if err != nil || major != 15 {
			t.Fatalf("currentMacOSMajor() = %d, %v; want 15, nil", major, err)
		}
	}

	if calls != 1 {
		t.Fatalf("sw_vers ran %d times, want 1", calls)
	}
}

func TestCurrentMacOSMajorDoesNotCacheFailure(t *testing.T) {
	calls := 0

	stubMacOSVersion(t, func(context.Context) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("signal: killed")
		}

		return []byte("14.2\n"), nil
	})

	if _, err := currentMacOSMajor(); err == nil {
		t.Fatal("first call: expected the sw_vers failure")
	}

	major, err := currentMacOSMajor()
	if err != nil || major != 14 {
		t.Fatalf("second call = %d, %v; want 14, nil", major, err)
	}
}

func TestCurrentMacOSMajorAllowsSlowSwVers(t *testing.T) {
	var budget time.Duration

	stubMacOSVersion(t, func(ctx context.Context) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("sw_vers context has no deadline")
		}

		budget = time.Until(deadline)

		return []byte("15.0\n"), nil
	})

	if _, err := currentMacOSMajor(); err != nil {
		t.Fatal(err)
	}

	if budget < 5*time.Second {
		t.Fatalf("sw_vers budget = %s, want at least 5s so a loaded runner does not kill it", budget)
	}
}

func TestCurrentMacOSMajorRejectsUnparsableVersion(t *testing.T) {
	stubMacOSVersion(t, func(context.Context) ([]byte, error) {
		return []byte("thrawn\n"), nil
	})

	if _, err := currentMacOSMajor(); err == nil {
		t.Fatal("expected a parse error")
	}
}
