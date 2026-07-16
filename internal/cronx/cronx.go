// Package cronx is graith's single, owned abstraction for cron schedules. It
// exists so config validation and daemon execution parse cron with exactly one
// grammar — before this package the two lived in separate robfig parsers
// (config.cronSpecParser and daemon.cronParser) that could silently drift.
//
// The grammar is deliberately narrow and matches what the docs promise: a
// five-field expression, or one of four descriptors. See Grammar for the full
// contract. Anything outside it — six/seven-field forms (seconds/year), the
// undocumented robfig descriptors (@yearly/@annually/@midnight/@reboot/@every),
// and Quartz/gronx extensions (?, L, W, #) — is rejected, so what config
// validation accepts is exactly what the runtime can fire.
//
// The engine underneath is robfig/cron/v3, used only as a parser + Next()
// (never its scheduler). We evaluated replacing it with github.com/adhocore/gronx
// (issue #1213) and rejected the swap: gronx v1.20.0 (the latest) computes wrong
// next-fire times — ones its own IsDue rejects — for day-of-month values absent
// in the current month (29/30/31), leap-day Feb 29, and DST fall-back
// transitions. The differential corpus in cronx_diff_test.go pins that evidence
// and acts as a tripwire if a future gronx fixes it. See
// docs/design/2026-07-16-cron-parser-evaluation.md.
package cronx

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Grammar documents the exact cron grammar graith accepts. It is the single
// source of truth referenced by the config docs and the trigger docs.
const Grammar = `graith cron grammar:

Five space-separated fields:

    ┌───────────── minute        (0-59)
    │ ┌─────────── hour          (0-23)
    │ │ ┌───────── day-of-month  (1-31)
    │ │ │ ┌─────── month         (1-12 or JAN-DEC)
    │ │ │ │ ┌───── day-of-week   (0-6 or SUN-SAT; 0 = Sunday)
    │ │ │ │ │
    * * * * *

Each field supports * (any), lists (1,15), ranges (1-5), and steps (*/15,
0-30/10). Month and day-of-week accept three-letter English names. Day-of-month
and day-of-week combine with OR semantics when both are restricted.

Or one of four descriptors:

    @hourly   →  0 * * * *
    @daily    →  0 0 * * *
    @weekly   →  0 0 * * 0
    @monthly  →  0 0 1 * *

Deliberately NOT accepted (tightened from what robfig/cron would parse):
  - seconds or year fields (six/seven-field forms)
  - @yearly, @annually, @midnight, @reboot, @every <dur>
  - Quartz/gronx extensions: ? L W #
  - Sunday as 7 (use 0)`

// allowedDescriptors maps graith's four supported descriptors to the canonical
// five-field expression they stand for. It is the allowlist that tightens
// robfig's broader descriptor set down to the documented four.
var allowedDescriptors = map[string]string{
	"@hourly":  "0 * * * *",
	"@daily":   "0 0 * * *",
	"@weekly":  "0 0 * * 0",
	"@monthly": "0 0 1 * *",
}

// rejectedChars are Quartz/gronx field extensions that robfig's standard parser
// also rejects; we reject them explicitly so the error names the character
// rather than leaking a parser-internal message.
const rejectedChars = "?LlWw#"

// specParser is the single robfig parser instance. Five positional fields plus
// descriptor support — but which descriptors reach it is gated by Parse, so the
// effective grammar is exactly Grammar above.
var specParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Schedule is a parsed graith cron schedule. The zero value is not usable;
// obtain one from Parse.
type Schedule struct {
	spec  string // the original spec, for diagnostics
	inner cron.Schedule
}

// Parse validates spec against Grammar and returns a Schedule. A returned error
// is safe to surface to users: it explains what was rejected.
func Parse(spec string) (Schedule, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return Schedule{}, errors.New("empty cron expression")
	}

	if strings.HasPrefix(trimmed, "@") {
		canonical, ok := allowedDescriptors[strings.ToLower(trimmed)]
		if !ok {
			return Schedule{}, fmt.Errorf("unsupported descriptor %q (want one of @hourly, @daily, @weekly, @monthly)", trimmed)
		}

		trimmed = canonical
	} else {
		if err := validateFields(trimmed); err != nil {
			return Schedule{}, err
		}
	}

	inner, err := specParser.Parse(trimmed)
	if err != nil {
		return Schedule{}, fmt.Errorf("invalid cron %q: %w", spec, err)
	}

	return Schedule{spec: spec, inner: inner}, nil
}

// Validate reports whether spec is a well-formed graith cron expression,
// returning the same descriptive error Parse would. Config validation uses it so
// it rejects exactly what the runtime cannot fire.
func Validate(spec string) error {
	_, err := Parse(spec)

	return err
}

// validateFields enforces the five-field shape and rejects the Quartz/gronx
// extensions before robfig sees the spec, so the count and character rules are
// graith's own contract rather than an incidental robfig behaviour.
func validateFields(spec string) error {
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return fmt.Errorf("cron %q must have exactly 5 fields (got %d); seconds and year fields are not supported", spec, len(fields))
	}

	if i := strings.IndexAny(spec, rejectedChars); i >= 0 {
		return fmt.Errorf("cron %q uses unsupported character %q (?, L, W, and # are not supported)", spec, spec[i:i+1])
	}

	return nil
}

// Next returns the next activation time strictly after t, in t's location. It
// returns the zero time if the schedule has no next activation (e.g. an
// impossible date like Feb 30) — callers must treat a zero return as "never
// fires" rather than "fire now".
func (s Schedule) Next(t time.Time) time.Time {
	return s.inner.Next(t)
}

// String returns the original spec, for logging and diagnostics.
func (s Schedule) String() string { return s.spec }
