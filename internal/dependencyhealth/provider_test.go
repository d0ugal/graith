package dependencyhealth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatuspagePollPartialIncidentFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "summary.json") {
			_, _ = w.Write([]byte(`{"indicator":"major","status":"Major outage"}`))
			return
		}

		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client := &http.Client{Transport: rewriteTransport{base: server.URL, transport: server.Client().Transport}}

	observation, err := (Statuspage{Client: client}).Poll(context.Background(), ServiceConfig{Name: "braw", BaseURL: "https://status.example"})
	if err != nil {
		t.Fatal(err)
	}

	if observation.State != Down || observation.SourceHealth != Fresh {
		t.Fatalf("observation = %#v", observation)
	}

	if got := normalizeIndicator("major"); got != Down {
		t.Fatalf("state = %q, want down", got)
	}

	if got := normalizeIndicator("mystery"); got != Unknown {
		t.Fatalf("unknown state = %q", got)
	}

	if _, ok := normalizeIncidentID("hostile id"); ok {
		t.Fatal("accepted hostile incident ID")
	}
}

func TestStatuspagePollAcceptsStatuspageStatusShapes(t *testing.T) {
	tests := map[string]struct {
		indicator  string
		status     string
		wantLabel  string
		wantErrMsg string
	}{
		"legacy string":  {indicator: "none", status: `"All Systems Operational"`, wantLabel: "All Systems Operational"},
		"current object": {status: `{"indicator":"none","description":"All Systems Operational"}`, wantLabel: "All Systems Operational"},
		"invalid number": {status: `42`, wantErrMsg: "cannot unmarshal number"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")

				if strings.HasSuffix(r.URL.Path, "summary.json") {
					_, _ = w.Write([]byte(`{"indicator":"` + test.indicator + `","status":` + test.status + `}`))
					return
				}

				_, _ = w.Write([]byte(`{"incidents":[]}`))
			}))
			defer server.Close()

			client := &http.Client{Transport: rewriteTransport{base: server.URL, transport: server.Client().Transport}}

			observation, err := (Statuspage{Client: client}).Poll(context.Background(), ServiceConfig{Name: "braw", BaseURL: "https://status.example"})
			if test.wantErrMsg != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErrMsg) {
					t.Fatalf("Poll() error = %v, want %q", err, test.wantErrMsg)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if observation.SourceHealth != Fresh || observation.State != Operational || observation.StatusLabel != test.wantLabel {
				t.Fatalf("observation = %#v", observation)
			}
		})
	}
}

func TestPollerPersistsCurrentStatuspageStatusShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "summary.json") {
			_, _ = w.Write([]byte(`{"status":{"indicator":"major","description":"Major system outage"}}`))
			return
		}

		_, _ = w.Write([]byte(`{"incidents":[]}`))
	}))
	defer server.Close()

	client := &http.Client{Transport: rewriteTransport{base: server.URL, transport: server.Client().Transport}}
	poller := NewPoller(Statuspage{Client: client}, []ServiceConfig{{Name: "braw", BaseURL: "https://status.example"}})
	poller.PollOnce(context.Background())

	snapshot := poller.Snapshot()

	if len(snapshot) != 1 {
		t.Fatalf("snapshot = %#v, want one observation", snapshot)
	}

	if got := snapshot[0]; got.SourceHealth != Fresh || got.State != Down || got.StatusLabel != "Major system outage" || got.LastSuccessAt.IsZero() {
		t.Fatalf("persisted observation = %#v", got)
	}
}

type rewriteTransport struct {
	base      string
	transport http.RoundTripper
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copyReq := req.Clone(req.Context())
	base, _ := http.NewRequest(http.MethodGet, t.base, nil)
	copyReq.URL.Scheme, copyReq.URL.Host = base.URL.Scheme, base.URL.Host

	return t.transport.RoundTrip(copyReq)
}

func TestGetJSONResponseLimitAndContentType(t *testing.T) {
	tests := map[string]struct {
		body        string
		contentType string
		wantErr     string
	}{
		"wrong content type": {body: `{}`, contentType: "text/html", wantErr: "content type"},
		"oversized":          {body: `{"x":"` + strings.Repeat("a", int(MaxResponseBytes)) + `"}`, contentType: "application/json", wantErr: "exceeds"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			client := &http.Client{}

			var target map[string]any

			err := getJSON(context.Background(), client, server.URL, &target)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
