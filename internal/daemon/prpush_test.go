package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

//nolint:wsl_v5 // request construction is intentionally compact test setup.
func pushRequest(t *testing.T, p *prPushState, event, delivery, body string) *http.Request {
	t.Helper()

	mac := hmac.New(sha256.New, []byte(p.secret))
	_, _ = io.WriteString(mac, body)

	r, err := http.NewRequest(http.MethodPost, "http://localhost"+p.route, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	r.Header.Set("X-GitHub-Delivery", delivery)
	r.Header.Set("X-GitHub-Event", event)
	return r
}

//nolint:wsl_v5 // table-driven assertions keep the test readable.
func TestPRPushReceiverAuthenticatesAndDeduplicates(t *testing.T) {
	p := newPRPushState(config.PRWatchPushConfig{Enabled: true, Repositories: []string{"d0ugal/graith"}, QueueSize: 4})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	defer p.stop()
	body := `{"repository":{"full_name":"d0ugal/graith"},"number":7,"pull_request":{"number":7,"head":{"sha":"braw"}}}`
	for _, tc := range []struct{ name, sig, status string }{{"valid", "valid", "accepted"}, {"replay", "valid", "accepted"}, {"bad signature", "bad", "rejected"}} {
		r := pushRequest(t, p, "pull_request", "dreich-1", body)
		if tc.sig == "bad" {
			r.Header.Set("X-Hub-Signature-256", "sha256=00")
		}
		rw := newCaptureResponse()
		p.handle(rw, r)
		if tc.status == "accepted" && rw.code != http.StatusAccepted {
			t.Errorf("%s status=%d", tc.name, rw.code)
		}
		if tc.status == "rejected" && rw.code == http.StatusAccepted {
			t.Errorf("%s accepted", tc.name)
		}
	}
	if got := len(p.hints); got != 1 {
		t.Fatalf("hints=%d, want one after replay", got)
	}
	stats := p.stats.snapshot()
	if stats.Accepted != 1 || stats.Rejected != 1 || stats.Duplicate != 1 {
		t.Fatalf("stats accepted=%d rejected=%d duplicate=%d", stats.Accepted, stats.Rejected, stats.Duplicate)
	}
}

//nolint:wsl_v5 // table-driven rejection assertions.
func TestPRPushReceiverRejectsUntrustedShape(t *testing.T) {
	p := newPRPushState(config.PRWatchPushConfig{Enabled: true, Repositories: []string{"d0ugal/graith"}, BodyMaxBytes: 8})
	p.secret = "secret"
	p.route = "/canny"
	for _, tc := range []struct{ name, event, body string }{{"event", "push", `{}`}, {"json", "pull_request", `not-json`}, {"repo", "pull_request", `{"repository":{"full_name":"other/repo"}}`}, {"large", "pull_request", "0123456789"}} {
		r := pushRequest(t, p, tc.event, tc.name, tc.body)
		rw := newCaptureResponse()
		p.handle(rw, r)
		if rw.code == http.StatusAccepted {
			t.Errorf("%s accepted", tc.name)
		}
	}
}

//nolint:wsl_v5 // table-driven assertions keep the test readable.
func TestPRPushReceiverCoalescesSamePRBurstAndRejectsUntargetedHint(t *testing.T) {
	p := newPRPushState(config.PRWatchPushConfig{Enabled: true, Repositories: []string{"d0ugal/graith"}, Debounce: "1m"})
	p.secret = "secret"
	p.route = "/canny"

	body := `{"repository":{"full_name":"d0ugal/graith"},"issue":{"number":7}}`

	for _, delivery := range []string{"braw-1", "braw-2"} {
		rw := newCaptureResponse()
		p.handle(rw, pushRequest(t, p, "issue_comment", delivery, body))
		if rw.code != http.StatusAccepted {
			t.Fatalf("delivery %s status=%d", delivery, rw.code)
		}
	}
	if got := len(p.hints); got != 1 {
		t.Fatalf("hints=%d, want one coalesced hint", got)
	}

	untargeted := `{"repository":{"full_name":"d0ugal/graith"}}`
	rw := newCaptureResponse()
	p.handle(rw, pushRequest(t, p, "check_suite", "braw-3", untargeted))
	if rw.code == http.StatusAccepted {
		t.Fatal("untargeted check suite accepted")
	}
}

//nolint:wsl_v5 // bounded-state assertions keep the test readable.
func TestPRPushReceiverBoundsPendingCoalescingKeys(t *testing.T) {
	p := newPRPushState(config.PRWatchPushConfig{Enabled: true, Repositories: []string{"d0ugal/graith"}, QueueSize: 1, DedupeSize: 2, Debounce: "1h"})
	p.secret = "secret"
	p.route = "/canny"

	for number := 1; number <= 8; number++ {
		body := fmt.Sprintf(`{"repository":{"full_name":"d0ugal/graith"},"number":%d}`, number)
		rw := newCaptureResponse()
		p.handle(rw, pushRequest(t, p, "pull_request", fmt.Sprintf("braw-%d", number), body))
		if rw.code != http.StatusAccepted {
			t.Fatalf("delivery %d status=%d", number, rw.code)
		}
	}
	if got, want := len(p.pending), 1; got > want {
		t.Fatalf("pending keys=%d, want at most %d", got, want)
	}
}

func TestPRPushForwardEventsIncludeAllSupportedHints(t *testing.T) {
	for _, event := range []string{"check_run", "check_suite", "pull_request", "pull_request_review", "pull_request_review_comment", "issue_comment"} {
		if !strings.Contains(pushForwardEvents, event) {
			t.Errorf("forward events missing %q: %s", event, pushForwardEvents)
		}
	}
}

//nolint:wsl_v5 // compact configured-argv assertion.
func TestPRPushForwardArgsExpandConfiguredTemplate(t *testing.T) {
	push := config.PRWatchPushConfig{ForwardArgs: []string{
		"webhook", "forward", "--repo", "{repository}", "--events", "{events}",
		"--url", "{url}", "--secret", "{secret}",
	}}
	args := push.ExpandedForwardArgs("d0ugal/graith", "check_run", "http://127.0.0.1:1234/route", "braw-secret")
	want := []string{"webhook", "forward", "--repo", "d0ugal/graith", "--events", "check_run", "--url", "http://127.0.0.1:1234/route", "--secret", "braw-secret"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("expanded args = %#v, want %#v", args, want)
	}
}

//nolint:wsl_v5 // compact bounds assertions.
func TestPRPushConfigBoundsAndLoopback(t *testing.T) {
	p := config.PRWatchPushConfig{BindAddress: "0.0.0.0:1", BodyMaxBytes: 99 << 20, QueueSize: 99 << 20, DedupeSize: 99 << 20}
	if got := p.LoopbackAddress(); got != "127.0.0.1:0" {
		t.Fatalf("address=%q", got)
	}
	if p.BodyLimit() != config.PRWatchPushBodyMaxBytes || p.QueueLimit() != config.PRWatchPushQueueMax || p.DedupeLimit() != config.PRWatchPushDedupeMax {
		t.Fatal("bounds not enforced")
	}
	if p.DedupeDuration() <= 0 || p.DebounceDuration() <= 0 {
		t.Fatal("duration bounds not enforced")
	}
}

type captureResponse struct {
	code   int
	header http.Header
	body   strings.Builder
}

func newCaptureResponse() *captureResponse      { return &captureResponse{header: make(http.Header)} }
func (w *captureResponse) Header() http.Header  { return w.header }
func (w *captureResponse) WriteHeader(code int) { w.code = code }

//nolint:wsl_v5 // response writer setup is intentionally compact test plumbing.
func (w *captureResponse) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.body.Write(b)
}
