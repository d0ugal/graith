// Package dependencyhealth contains the provider-neutral dependency health
// configuration and durable observation model. Network polling and delivery
// are intentionally outside this package.
package dependencyhealth

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	DefaultPollInterval         = 5 * time.Minute
	DefaultRecoveryPollInterval = 30 * time.Second
	DefaultTimeout              = 5 * time.Second
	MaxServices                 = 32
	MaxPollConcurrency          = 4
	StateSchemaVersion          = 1
)

var serviceNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type Config struct {
	Enabled              bool      `toml:"enabled"`
	PollInterval         string    `toml:"poll_interval"`
	RecoveryPollInterval string    `toml:"recovery_poll_interval"`
	Timeout              string    `toml:"timeout"`
	Services             []Service `toml:"service"`
}

type Service struct {
	Name       string   `toml:"name"`
	Provider   string   `toml:"provider"`
	BaseURL    string   `toml:"base_url"`
	Global     bool     `toml:"global"`
	AgentTypes []string `toml:"agent_types"`
}

func (c Config) PollIntervalDuration() time.Duration {
	return durationOr(c.PollInterval, DefaultPollInterval)
}
func (c Config) RecoveryPollIntervalDuration() time.Duration {
	return durationOr(c.RecoveryPollInterval, DefaultRecoveryPollInterval)
}
func (c Config) TimeoutDuration() time.Duration { return durationOr(c.Timeout, DefaultTimeout) }

func durationOr(raw string, fallback time.Duration) time.Duration {
	d, err := parseDurationWithDays(raw)
	if err != nil || d <= 0 {
		return fallback
	}

	return d
}

// parseDurationWithDays mirrors the config package's duration syntax without
// importing config (which imports this package). It supports an integer day
// prefix followed by any time.ParseDuration remainder, such as "1d2h".
func parseDurationWithDays(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)

	var total time.Duration

	if i := strings.Index(s, "d"); i > 0 {
		var days int

		if _, err := fmt.Sscanf(s[:i+1], "%dd", &days); err == nil {
			total = time.Duration(days) * 24 * time.Hour
			s = s[i+1:]
		}
	}

	if s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, err
		}

		total += d
	}

	if total < 0 {
		return 0, errors.New("negative duration not allowed")
	}

	return total, nil
}

func (c Config) Validate(parseDuration func(string) (time.Duration, error), configuredAgents map[string]struct{}) error {
	var errs []error
	if len(c.Services) > MaxServices {
		errs = append(errs, fmt.Errorf("dependency_health.service: at most %d entries", MaxServices))
	}

	normal, recovery := c.PollIntervalDuration(), c.RecoveryPollIntervalDuration()
	if strings.TrimSpace(c.PollInterval) != "" {
		if d, err := parseDuration(c.PollInterval); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("dependency_health.poll_interval %q: must be a positive duration", c.PollInterval))
		} else {
			normal = d
		}
	}

	if strings.TrimSpace(c.RecoveryPollInterval) != "" {
		if d, err := parseDuration(c.RecoveryPollInterval); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("dependency_health.recovery_poll_interval %q: must be a positive duration", c.RecoveryPollInterval))
		} else {
			recovery = d
		}
	}

	if recovery > normal {
		errs = append(errs, errors.New("dependency_health.recovery_poll_interval must not exceed poll_interval"))
	}

	if strings.TrimSpace(c.Timeout) != "" {
		if d, err := parseDuration(c.Timeout); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("dependency_health.timeout %q: must be a positive duration", c.Timeout))
		}
	}

	seen := make(map[string]struct{}, len(c.Services))
	for i, service := range c.Services {
		prefix := fmt.Sprintf("dependency_health.service[%d]", i)

		name := strings.TrimSpace(service.Name)
		if !serviceNameRE.MatchString(name) {
			errs = append(errs, fmt.Errorf("%s.name %q: must be 1-64 safe printable characters", prefix, service.Name))
		}

		if _, ok := seen[name]; ok {
			errs = append(errs, fmt.Errorf("%s.name %q: duplicate", prefix, name))
		}

		seen[name] = struct{}{}

		if service.Provider != "statuspage" {
			errs = append(errs, fmt.Errorf("%s.provider %q: unsupported provider", prefix, service.Provider))
		}

		if err := validateBaseURL(service.BaseURL); err != nil {
			errs = append(errs, fmt.Errorf("%s.base_url: %w", prefix, err))
		}

		if service.Global && len(service.AgentTypes) > 0 {
			errs = append(errs, fmt.Errorf("%s: global services must not set agent_types", prefix))
		}

		if !service.Global && len(service.AgentTypes) == 0 {
			errs = append(errs, fmt.Errorf("%s: non-global services require agent_types", prefix))
		}

		seenAgents := map[string]struct{}{}

		for _, agent := range service.AgentTypes {
			agent = strings.TrimSpace(agent)
			if agent == "" {
				errs = append(errs, fmt.Errorf("%s.agent_types: names must not be empty", prefix))
				continue
			}

			if _, ok := seenAgents[agent]; ok {
				errs = append(errs, fmt.Errorf("%s.agent_types: duplicate %q", prefix, agent))
			}

			seenAgents[agent] = struct{}{}
			if len(configuredAgents) > 0 {
				if _, ok := configuredAgents[agent]; !ok {
					errs = append(errs, fmt.Errorf("%s.agent_types: %q is not a configured agent", prefix, agent))
				}
			}
		}
	}

	if len(errs) > 0 {
		return joinErrors(errs)
	}

	return nil
}

func joinErrors(errs []error) error {
	parts := make([]string, len(errs))
	for i, err := range errs {
		parts[i] = err.Error()
	}

	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "https" || u.Host == "" {
		return errors.New("must be an HTTPS origin")
	}

	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("must not contain userinfo, path, query, or fragment")
	}

	if u.Port() != "" && u.Port() != "443" {
		return errors.New("must use the default HTTPS port")
	}

	host := u.Hostname()
	if host == "" {
		return errors.New("host is required")
	}

	if ip := net.ParseIP(host); ip != nil && unsafeIPLiteral(ip) {
		return errors.New("IP literal must be globally routable")
	}

	return nil
}

func unsafeIPLiteral(ip net.IP) bool {
	return !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified()
}

type ObservedState string

const (
	Operational ObservedState = "operational"
	Degraded    ObservedState = "degraded"
	Down        ObservedState = "down"
	Unknown     ObservedState = "unknown"
)

type SourceHealth string

const (
	Fresh  SourceHealth = "fresh"
	Stale  SourceHealth = "stale"
	Failed SourceHealth = "failed"
)

type ServiceState struct {
	ObservedState ObservedState `json:"observed_state"`
	SourceHealth  SourceHealth  `json:"source_health"`
	ObservedAt    time.Time     `json:"observed_at,omitempty"`
	LastSuccessAt *time.Time    `json:"last_success_at,omitempty"`
	LastFailureAt *time.Time    `json:"last_failure_at,omitempty"`
	IncidentIDs   []string      `json:"incident_ids,omitempty"`
	Generation    uint64        `json:"generation,omitempty"`
}

type Transition struct {
	Service         string        `json:"service"`
	Generation      uint64        `json:"generation"`
	ObservedState   ObservedState `json:"observed_state"`
	TargetSessionID string        `json:"target_session_id,omitempty"`
	NextAttemptAt   time.Time     `json:"next_attempt_at,omitempty"`
	Attempts        int           `json:"attempts,omitempty"`
}

type PersistedState struct {
	SchemaVersion int                     `json:"schema_version"`
	Services      map[string]ServiceState `json:"services,omitempty"`
	Outbox        []Transition            `json:"outbox,omitempty"`
}

// UnmarshalJSON isolates this optional envelope from the main daemon state.
// Invalid or future health data is intentionally treated as an empty baseline.
func (p *PersistedState) UnmarshalJSON(data []byte) error {
	type wire PersistedState

	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		slog.Warn("discarding corrupted dependency health state", "err", err)

		*p = PersistedState{}

		return nil
	}

	if decoded.SchemaVersion != StateSchemaVersion || len(decoded.Services) > MaxServices || len(decoded.Outbox) > MaxServices*32 {
		slog.Warn("discarding unsupported dependency health state", "schema_version", decoded.SchemaVersion)

		*p = PersistedState{}

		return nil
	}

	if decoded.Services == nil {
		decoded.Services = make(map[string]ServiceState)
	}

	for name, service := range decoded.Services {
		if !serviceNameRE.MatchString(name) || !validObservedState(service.ObservedState) || !validSourceHealth(service.SourceHealth) {
			slog.Warn("discarding invalid dependency health state", "service", name)

			*p = PersistedState{}

			return nil
		}
	}

	for _, transition := range decoded.Outbox {
		if !serviceNameRE.MatchString(transition.Service) || !validObservedState(transition.ObservedState) || transition.Attempts < 0 {
			slog.Warn("discarding invalid dependency health outbox")

			*p = PersistedState{}

			return nil
		}
	}

	*p = PersistedState(decoded)

	return nil
}

func validObservedState(state ObservedState) bool {
	return state == Operational || state == Degraded || state == Down || state == Unknown
}

func validSourceHealth(health SourceHealth) bool {
	return health == Fresh || health == Stale || health == Failed
}

func (p *PersistedState) Clone() *PersistedState {
	if p == nil {
		return nil
	}

	out := *p

	out.Services = make(map[string]ServiceState, len(p.Services))
	for k, v := range p.Services {
		v.IncidentIDs = append([]string(nil), v.IncidentIDs...)
		out.Services[k] = v
	}

	out.Outbox = append([]Transition(nil), p.Outbox...)

	return &out
}

func SortedServiceNames(states map[string]ServiceState) []string {
	names := make([]string, 0, len(states))
	for name := range states {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}
