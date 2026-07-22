package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMutationLeasesReportConcurrentHolders(t *testing.T) {
	now := time.Date(2026, time.July, 22, 20, 0, 0, 0, time.UTC)
	sm := &SessionManager{mutationNow: func() time.Time { return now }}

	first, err := sm.beginMutationRequest("msg_pub", "session(braw)")
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(1500 * time.Millisecond)

	second, err := sm.beginMutationRequest("data", "local-human")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	err = sm.waitMutationIdle(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitMutationIdle error = %v, want deadline", err)
	}

	var drainErr *mutationDrainError
	if !errors.As(err, &drainErr) {
		t.Fatalf("waitMutationIdle error = %T, want mutationDrainError", err)
	}

	if len(drainErr.holders) != 2 {
		t.Fatalf("holders = %d, want 2", len(drainErr.holders))
	}

	message := err.Error()
	for _, want := range []string{"active holders=2", "mutation-1", "mutation-2", "op=msg_pub", "op=data", "age=1.5s"} {
		if !strings.Contains(message, want) {
			t.Errorf("timeout message %q missing %q", message, want)
		}
	}

	sm.endMutationRequest(first)
	sm.endMutationRequest(first) // double-end must be harmless
	sm.endMutationRequest(second)

	if err := sm.waitMutationIdle(context.Background()); err != nil {
		t.Fatalf("leases did not clean up: %v", err)
	}
}

func TestMutationLeaseEndDoesNotRemoveAnotherHolder(t *testing.T) {
	sm := &SessionManager{}

	first, err := sm.beginMutationRequest("msg_pub", "session(braw)")
	if err != nil {
		t.Fatal(err)
	}

	second, err := sm.beginMutationRequest("msg_ack", "session(canny)")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{}, 2)

	go func() { sm.endMutationRequest(first); done <- struct{}{} }()
	go func() { sm.endMutationRequest(first); done <- struct{}{} }()

	<-done
	<-done

	holders := sm.mutationLeaseSnapshot()
	if len(holders) != 1 || holders[0].ID != second.id {
		t.Fatalf("holders after first end = %+v, want only %s", holders, second.id)
	}

	sm.endMutationRequest(second)
}

func TestMutationLeaseAdmissionRejectsUpgrade(t *testing.T) {
	sm := &SessionManager{}

	sm.upgradePending = true
	if lease, err := sm.beginMutationRequest("msg_pub", "local-human"); lease != nil || err == nil {
		t.Fatalf("begin during upgrade = (%v, %v), want rejection", lease, err)
	}
}

func TestPublicUpgradeFailureIncludesMutationHolders(t *testing.T) {
	err := &mutationDrainError{
		cause: context.DeadlineExceeded,
		holders: []mutationLeaseSummary{{
			ID: "mutation-7", Operation: "msg_ack", Caller: "session(braw)", Age: 2 * time.Second,
		}},
	}
	publicErr := publicUpgradeFailure("accepted daemon mutations did not drain before upgrade", err)

	message := publicErr.Error()
	for _, want := range []string{"mutation-7", "op=msg_ack", "caller=session(braw)", "age=2s"} {
		if !strings.Contains(message, want) {
			t.Errorf("public upgrade error %q missing %q", message, want)
		}
	}

	if got := strings.Count(message, "mutation-7"); got != 1 {
		t.Errorf("holder ID appears %d times in public upgrade error, want 1: %q", got, message)
	}

	if got := strings.Count(message, "mutation drain timed out"); got != 1 {
		t.Errorf("drain summary appears %d times in public upgrade error, want 1: %q", got, message)
	}

	if strings.Contains(message, "secret") {
		t.Fatalf("public upgrade error exposed sensitive text: %q", message)
	}

	if !errors.Is(publicErr, context.DeadlineExceeded) {
		t.Fatalf("public upgrade error lost deadline cause: %v", publicErr)
	}
}

func TestMutationLeaseMetadataIsBoundedAndPayloadFree(t *testing.T) {
	sm := &SessionManager{}
	sensitive := strings.Repeat("secret-token-and-body-", 20)

	lease, err := sm.beginMutationRequest("msg_pub", sensitive)
	if err != nil {
		t.Fatal(err)
	}

	message := (&mutationDrainError{holders: sm.mutationLeaseSnapshot()}).Error()
	if strings.Contains(message, sensitive) || sm.mutationLeaseSnapshot()[0].Caller != "unknown" {
		t.Fatalf("lease retained sensitive or unbounded caller metadata: %q", message)
	}

	sm.endMutationRequest(lease)
}
