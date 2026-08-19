package dependencyhealth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const MaxResponseBytes int64 = 256 * 1024

// ServiceConfig is the validated, runtime form of one dependency service.
// BaseURL must be an HTTPS origin; the provider owns the request paths.
type ServiceConfig struct {
	Name                 string
	BaseURL              string
	Timeout              time.Duration
	PollInterval         time.Duration
	RecoveryPollInterval time.Duration
}

type Observation struct {
	Service       string
	State         ObservedState
	SourceHealth  SourceHealth
	ObservedAt    time.Time
	LastSuccessAt time.Time
	LastFailureAt time.Time
	SourceURL     string
	IncidentIDs   []string
	StatusLabel   string
}

type Statuspage struct {
	Client *http.Client
	Now    func() time.Time
}

type summaryResponse struct {
	Indicator string `json:"indicator"`
	Status    string `json:"status"`
}

type incidentsResponse struct {
	Incidents []struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Name    string `json:"name"`
		Updates []struct {
			Body string `json:"body"`
		} `json:"incident_updates"`
	} `json:"incidents"`
}

func (p Statuspage) Poll(ctx context.Context, service ServiceConfig) (Observation, error) {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}

	base, err := validateOrigin(service.BaseURL)
	if err != nil {
		return Observation{}, err
	}

	client := p.Client
	if client == nil {
		client = safeHTTPClient(service.Timeout)
	}

	var summary summaryResponse
	if err := getJSON(ctx, client, base+"/api/v2/summary.json", &summary); err != nil {
		return Observation{}, err
	}

	state := normalizeIndicator(summary.Indicator)
	ids := make([]string, 0)
	// Incidents are useful evidence but not required for a valid summary. This
	// permits Statuspage installations that omit or temporarily reject the feed.
	var incidents incidentsResponse
	if err := getJSON(ctx, client, base+"/api/v2/incidents.json", &incidents); err == nil {
		for _, incident := range incidents.Incidents {
			if id, ok := normalizeIncidentID(incident.ID); ok {
				ids = append(ids, id)
			}
		}
	}

	return Observation{
		Service: service.Name, State: state, SourceHealth: Fresh, ObservedAt: now(),
		LastSuccessAt: now(), SourceURL: base, IncidentIDs: ids,
		StatusLabel: boundedLabel(summary.Status),
	}, nil
}

func normalizeIndicator(indicator string) ObservedState {
	switch strings.ToLower(strings.TrimSpace(indicator)) {
	case "none":
		return Operational
	case "minor":
		return Degraded
	case "major", "critical":
		return Down
	default:
		return Unknown
	}
}

func normalizeIncidentID(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if len(id) == 0 || len(id) > 128 {
		return "", false
	}

	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r) {
			continue
		}

		return "", false
	}

	return id, true
}

func boundedLabel(label string) string {
	label = strings.TrimSpace(label)
	if len(label) > 128 {
		label = label[:128]
	}

	return label
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("statuspage returned HTTP %d", resp.StatusCode)
	}

	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		return fmt.Errorf("statuspage returned content type %q", contentType)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if int64(len(body)) > MaxResponseBytes {
		return errors.New("statuspage response exceeds limit")
	}

	if err != nil {
		return fmt.Errorf("read statuspage response: %w", err)
	}

	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode statuspage response: %w", err)
	}

	return nil
}

func validateOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return "", errors.New("statuspage base URL must be an HTTPS origin")
	}

	host := u.Hostname()
	if host == "" {
		return "", errors.New("statuspage base URL has no host")
	}

	if port := u.Port(); port != "" && port != "443" {
		return "", errors.New("statuspage base URL must use the default HTTPS port")
	}

	if ip := net.ParseIP(host); ip != nil && !isAllowedIP(ip) {
		return "", errors.New("statuspage base URL must use a global hostname")
	}

	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	u.Host = host

	return strings.TrimSuffix(u.String(), "/"), nil
}

func safeHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: dialValidated,
	}}
}

func dialValidated(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	if ip := net.ParseIP(host); ip != nil {
		if !isAllowedIP(ip) {
			return nil, errors.New("refusing non-global destination")
		}

		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}

	for _, candidate := range ips {
		if !isAllowedIP(candidate.IP) {
			continue
		}

		conn, dialErr := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), strconv.Itoa(mustPort(port))))
		if dialErr == nil {
			return conn, nil
		}
	}

	return nil, errors.New("hostname has no global address")
}

func isAllowedIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified()
}

func mustPort(port string) int { n, _ := strconv.Atoi(port); return n }
