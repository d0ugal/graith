package daemon

// prpush.go is the experimental, loopback-only webhook hint transport. A
// delivery is never trusted as state: it is authenticated and reduced to a
// repository/PR/head hint, then sent through the ordinary authoritative poll.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/tools"
)

type prPushHint struct {
	Repository string
	Number     int
	HeadSHA    string
}
type prPushEnvelope struct {
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Number int `json:"number"`
	Issue  struct {
		Number int `json:"number"`
	} `json:"issue"`
	PullRequest struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	CheckRun struct {
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number  int    `json:"number"`
			HeadSHA string `json:"head_sha"`
		} `json:"pull_requests"`
	} `json:"check_run"`
	CheckSuite struct {
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
}

type prPushStats struct {
	mu                                                       sync.Mutex
	state                                                    string
	lastError                                                string
	lastDelivery                                             time.Time
	accepted, rejected, duplicate, coalesced, dropped, kicks uint64
}

type prPushStatsSnapshot struct {
	State                                                    string
	LastError                                                string
	LastDelivery                                             time.Time
	Accepted, Rejected, Duplicate, Coalesced, Dropped, Kicks uint64
}

func (s *prPushStats) snapshot() prPushStatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	return prPushStatsSnapshot{s.state, s.lastError, s.lastDelivery, s.accepted, s.rejected, s.duplicate, s.coalesced, s.dropped, s.kicks}
}

type prPushState struct {
	mu            sync.Mutex
	cfg           config.PRWatchPushConfig
	route, secret string
	server        *http.Server
	listener      net.Listener
	hints         chan prPushHint
	seen          map[string]time.Time
	pending       map[string]time.Time
	stats         prPushStats
}

const pushForwardEvents = "check_run,check_suite,pull_request,pull_request_review,pull_request_review_comment,issue_comment"

func newPRPushState(cfg config.PRWatchPushConfig) *prPushState {
	return &prPushState{cfg: cfg, hints: make(chan prPushHint, cfg.QueueLimit()), seen: make(map[string]time.Time), pending: make(map[string]time.Time)}
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return hex.EncodeToString(b), nil
}

//nolint:wsl_v5 // lifecycle setup is intentionally linear.
func (p *prPushState) start(ctx context.Context, sm *SessionManager) error {
	if !p.cfg.Enabled || len(p.cfg.Repositories) == 0 {
		p.stats.mu.Lock()
		p.stats.state = "disabled"
		p.stats.mu.Unlock()
		return nil
	}
	secret := p.cfg.Secret
	var err error
	if secret == "" {
		secret, err = randomToken(32)
		if err != nil {
			return err
		}
	}
	route := p.cfg.Route
	if len(route) < 24 {
		route, err = randomToken(24)
		if err != nil {
			return err
		}
	}
	route = "/" + strings.Trim(route, "/")
	ln, err := net.Listen("tcp", p.cfg.LoopbackAddress())
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.secret, p.route, p.listener = secret, route, ln
	p.server = &http.Server{
		Handler:           http.HandlerFunc(p.handle),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
	}
	p.stats.mu.Lock()
	p.stats.state = "ready"
	p.stats.mu.Unlock()
	p.mu.Unlock()
	go func() { <-ctx.Done(); _ = p.server.Close() }()
	go func() {
		if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			p.stats.mu.Lock()
			p.stats.state = "degraded"
			p.stats.lastError = err.Error()
			p.stats.mu.Unlock()
		}
	}()
	go p.forward(ctx)
	return nil
}

// forward owns one best-effort gh webhook forwarder per configured repository.
// The extension is intentionally not installed or granted permissions by
// graith; an absent extension simply leaves polling as the fallback.
//
//nolint:wsl_v5 // retry loop is intentionally linear.
func (p *prPushState) forward(ctx context.Context) {
	p.mu.Lock()
	endpoint := "http://" + p.listener.Addr().String() + p.route
	secret := p.secret
	repos := append([]string(nil), p.cfg.Repositories...)
	p.mu.Unlock()
	for _, repo := range repos {
		go func(repo string) {
			backoff := time.Second
			for {
				if ctx.Err() != nil {
					return
				}
				args := p.cfg.ExpandedForwardArgs(repo, pushForwardEvents, endpoint, secret)
				if len(args) == 0 {
					p.stats.mu.Lock()
					p.stats.state = "degraded"
					p.stats.lastError = "pr_watch.push.forward_args is empty"
					p.stats.mu.Unlock()
					return
				}
				cmd := exec.CommandContext(ctx, tools.GH(), args...)
				if err := cmd.Run(); err != nil {
					p.stats.mu.Lock()
					p.stats.state = "degraded"
					p.stats.lastError = err.Error()
					p.stats.mu.Unlock()
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < time.Minute {
					backoff *= 2
				}
			}
		}(repo)
	}
}

func (p *prPushState) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.server != nil {
		_ = p.server.Close()
	}

	if p.listener != nil {
		_ = p.listener.Close()
	}
}

//nolint:wsl_v5 // request validation is intentionally linear and fail-closed.
func (p *prPushState) handle(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	route, secret := p.route, p.secret
	p.mu.Unlock()
	if r.URL.Path != route || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(p.cfg.BodyLimit())+1))
	if err != nil || len(body) > p.cfg.BodyLimit() {
		p.reject(w, "oversized body")
		return
	}
	sig := r.Header.Get("X-Hub-Signature-256")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		p.reject(w, "invalid signature")
		return
	}
	delivery := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	event := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	if delivery == "" || len(delivery) > 256 || !supportedPushEvent(event) {
		p.reject(w, "invalid delivery")
		return
	}
	var env prPushEnvelope
	if json.Unmarshal(body, &env) != nil {
		p.reject(w, "malformed payload")
		return
	}
	repo := strings.ToLower(strings.TrimSpace(env.Repository.FullName))
	if !p.allowed(repo) {
		p.reject(w, "repository not allowed")
		return
	}
	number := env.Number
	sha := env.PullRequest.Head.SHA
	if number == 0 {
		number = env.PullRequest.Number
	}
	if number == 0 && event == "check_run" && len(env.CheckRun.PullRequests) > 0 {
		number = env.CheckRun.PullRequests[0].Number
		sha = env.CheckRun.PullRequests[0].HeadSHA
	}
	if number == 0 && event == "check_suite" && len(env.CheckSuite.PullRequests) > 0 {
		number = env.CheckSuite.PullRequests[0].Number
		sha = env.CheckSuite.HeadSHA
	}
	if number == 0 && event == "issue_comment" {
		number = env.Issue.Number
	}
	if number <= 0 {
		p.reject(w, "missing pull request number")
		return
	}
	if len(sha) > 256 {
		p.reject(w, "head SHA too long")
		return
	}
	now := time.Now()
	p.mu.Lock()
	for id, at := range p.seen {
		if now.Sub(at) > p.cfg.DedupeDuration() {
			delete(p.seen, id)
		}
	}
	if _, ok := p.seen[delivery]; ok {
		p.mu.Unlock()
		p.stats.mu.Lock()
		p.stats.duplicate++
		p.stats.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if len(p.seen) >= p.cfg.DedupeLimit() {
		var oldestID string
		var oldest time.Time
		for id, at := range p.seen {
			if oldestID == "" || at.Before(oldest) {
				oldestID, oldest = id, at
			}
		}
		if oldestID != "" {
			delete(p.seen, oldestID)
		}
	}
	p.seen[delivery] = now
	key := repo + "#" + strconv.Itoa(number) + "#" + sha
	for pendingKey, at := range p.pending {
		if now.Sub(at) > p.cfg.DebounceDuration() {
			delete(p.pending, pendingKey)
		}
	}
	pendingLimit := p.cfg.QueueLimit()
	if dedupeLimit := p.cfg.DedupeLimit(); dedupeLimit < pendingLimit {
		pendingLimit = dedupeLimit
	}
	if len(p.pending) >= pendingLimit {
		var oldestKey string
		var oldest time.Time
		for pendingKey, at := range p.pending {
			if oldestKey == "" || at.Before(oldest) {
				oldestKey, oldest = pendingKey, at
			}
		}
		if oldestKey != "" {
			delete(p.pending, oldestKey)
		}
	}
	if at, ok := p.pending[key]; ok && now.Sub(at) <= p.cfg.DebounceDuration() {
		p.mu.Unlock()
		p.stats.mu.Lock()
		p.stats.coalesced++
		p.stats.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		return
	}
	p.pending[key] = now
	p.mu.Unlock()
	p.stats.mu.Lock()
	p.stats.accepted++
	p.stats.lastDelivery = now
	p.stats.mu.Unlock()
	select {
	case p.hints <- prPushHint{Repository: repo, Number: number, HeadSHA: sha}:
	default:
		p.stats.mu.Lock()
		p.stats.dropped++
		p.stats.mu.Unlock()
	}
	w.WriteHeader(http.StatusAccepted)
}
func (p *prPushState) reject(w http.ResponseWriter, reason string) {
	p.stats.mu.Lock()
	p.stats.rejected++
	p.stats.lastError = reason
	p.stats.mu.Unlock()
	http.Error(w, "rejected", http.StatusUnauthorized)
}
func (p *prPushState) allowed(repo string) bool {
	for _, v := range p.cfg.Repositories {
		if strings.EqualFold(strings.TrimSpace(v), repo) {
			return true
		}
	}

	return false
}
func supportedPushEvent(v string) bool {
	switch v {
	case "check_run", "check_suite", "pull_request", "pull_request_review", "pull_request_review_comment", "issue_comment":
		return true
	}

	return false
}
