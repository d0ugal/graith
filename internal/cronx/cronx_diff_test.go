package cronx

import (
	"testing"
	"time"

	"github.com/adhocore/gronx"
	robfig "github.com/robfig/cron/v3"
)

// This file is the differential corpus for issue #1213: it compares the engine
// cronx wraps (robfig/cron/v3) against the proposed replacement
// (github.com/adhocore/gronx) and records the decision — KEEP robfig — with the
// evidence that forced it.
//
// It also acts as a tripwire. gronx is actively maintained; if a future release
// fixes the bugs pinned in TestGronxKnownBugs, that test's "engines must still
// disagree" assertions fail, signalling it's time to re-run this evaluation. And
// if gronx ever diverges from robfig on the safe corpus (TestEnginesAgree), that
// fails too — so the corpus can't silently rot.
//
// gronx is therefore a test-only dependency. It is NOT on any runtime path; the
// runtime uses cronx (robfig) exclusively.

var diffRobfigParser = robfig.NewParser(
	robfig.Minute | robfig.Hour | robfig.Dom | robfig.Month | robfig.Dow | robfig.Descriptor,
)

func robfigNext(t *testing.T, expr string, ref time.Time) time.Time {
	t.Helper()

	s, err := diffRobfigParser.Parse(expr)
	if err != nil {
		t.Fatalf("robfig parse %q: %v", expr, err)
	}

	return s.Next(ref)
}

// TestEnginesAgree walks a corpus of graith-grammar expressions that avoid the
// known-buggy inputs, across several IANA zones and reference instants, and
// asserts robfig and gronx compute the SAME next-fire. This is the positive half
// of the differential: on ordinary schedules the two engines are equivalent, so
// the reasons to keep robfig are exactly the divergences pinned below — nothing
// more.
func TestEnginesAgree(t *testing.T) {
	exprs := []string{
		"@hourly", "@daily", "@weekly", "@monthly",
		"* * * * *",
		"0 9 * * *",
		"*/15 * * * *",
		"30 8-17 * * *",
		"0 0 * * 0", // Sunday as 0 (both accept)
		"0 0 * * SUN",
		"0 0 1,15 * *",
		"0 12 * * 1-5",
		"15,45 8-17 * * MON-FRI",
		"0 0 1 * *", // safe day-of-month
		"0 0 * JAN,DEC 1",
		"23 0-20/2 * * *",
		"0 4 8-14 * *",
		"5 4 * * 0",
	}
	zones := []string{"UTC", "Europe/London", "Asia/Kolkata"} // Kolkata = no DST, +05:30
	refs := []string{
		"2026-01-01T00:00:00",
		"2026-06-15T12:34:00",
		"2026-02-27T23:59:00",
		"2026-12-31T23:59:00",
		"2027-07-04T06:15:00",
	}

	g := gronx.New()

	for _, zone := range zones {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("timezone %q unavailable: %v", zone, err)
		}

		for _, rs := range refs {
			ref, err := time.ParseInLocation("2006-01-02T15:04:05", rs, loc)
			if err != nil {
				t.Fatalf("bad ref %q: %v", rs, err)
			}

			for _, expr := range exprs {
				rNext := robfigNext(t, expr, ref)

				gNext, gErr := gronx.NextTickAfter(expr, ref, false)
				if gErr != nil {
					t.Errorf("gronx err on safe expr %q zone=%s ref=%s: %v", expr, zone, rs, gErr)
					continue
				}

				if !rNext.Equal(gNext) {
					t.Errorf("engines diverge on SAFE expr %q zone=%s ref=%s: robfig=%s gronx=%s (unexpected — investigate before trusting either)",
						expr, zone, rs, rNext.Format(time.RFC3339), gNext.Format(time.RFC3339))
				}

				// Sanity: gronx must at least consider its own answer due.
				if due, _ := g.IsDue(expr, gNext); !due {
					t.Errorf("gronx returned a non-due time for safe expr %q zone=%s ref=%s: %s", expr, zone, rs, gNext.Format(time.RFC3339))
				}
			}
		}
	}
}

// TestGronxKnownBugs pins the concrete cases where gronx v1.20.0 (the latest at
// evaluation time) computes a WRONG next-fire — a time its own IsDue rejects —
// while robfig is correct. These are why graith keeps robfig. Each case asserts:
//   - robfig produces the correct answer,
//   - gronx disagrees with robfig, and
//   - gronx's answer is not even due per gronx itself (so it's a bug, not a
//     defensible difference).
//
// If a future gronx fixes one of these, the "engines must disagree" assertion
// fails — re-open issue #1213 and re-evaluate the swap.
func TestGronxKnownBugs(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York unavailable: %v", err)
	}

	utc := time.UTC
	g := gronx.New()

	at := func(loc *time.Location, s string) time.Time {
		ts, err := time.ParseInLocation("2006-01-02T15:04:05", s, loc)
		if err != nil {
			t.Fatalf("bad time %q: %v", s, err)
		}

		return ts
	}

	cases := []struct {
		name, expr string
		ref        time.Time
		wantRobfig time.Time // the correct answer
		reason     string
	}{
		{
			name:       "day-of-month 31 overflow",
			expr:       "0 0 31 * *",
			ref:        at(utc, "2026-11-01T00:30:00"),
			wantRobfig: at(utc, "2026-12-31T00:00:00"),
			reason:     "gronx returns 2026-12-02 (not even day 31) — its per-field bump limit gives up crossing short months",
		},
		{
			name:       "leap-day Feb 29",
			expr:       "0 0 29 2 *",
			ref:        at(utc, "2026-01-01T00:00:00"),
			wantRobfig: at(utc, "2028-02-29T00:00:00"),
			reason:     "gronx returns 2026-03-04 — it cannot skip forward to the next leap year",
		},
		{
			name:       "DST fall-back day, plain monthly",
			expr:       "0 0 1 * *",
			ref:        at(ny, "2026-11-01T00:30:00"), // 2026-11-01 is US fall-back
			wantRobfig: at(ny, "2026-12-01T00:00:00"),
			reason:     "gronx returns 2026-11-02 — the fall-back transition corrupts the day search",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rNext := robfigNext(t, tc.expr, tc.ref)
			if !rNext.Equal(tc.wantRobfig) {
				t.Fatalf("robfig regressed on %q: got %s want %s", tc.expr, rNext.Format(time.RFC3339), tc.wantRobfig.Format(time.RFC3339))
			}

			gNext, gErr := gronx.NextTickAfter(tc.expr, tc.ref, false)
			if gErr != nil {
				// A future gronx that returns an error instead of a wrong answer is
				// an improvement, but still not correct — note and move on.
				t.Logf("%s: gronx now errors (%v) instead of returning a wrong time", tc.name, gErr)
				return
			}

			if rNext.Equal(gNext) {
				t.Fatalf("gronx now AGREES with robfig on %q (%s) — the bug appears fixed; re-evaluate issue #1213 (was: %s)",
					tc.expr, gNext.Format(time.RFC3339), tc.reason)
			}

			// The smoking gun: gronx's own IsDue rejects the time gronx returned.
			if due, _ := g.IsDue(tc.expr, gNext); due {
				t.Errorf("%s: gronx returned %s and considers it due — bug characterization changed, re-verify", tc.name, gNext.Format(time.RFC3339))
			} else {
				t.Logf("confirmed gronx bug: %q from %s -> gronx=%s (NOT due per gronx), robfig=%s. %s",
					tc.expr, tc.ref.Format(time.RFC3339), gNext.Format(time.RFC3339), rNext.Format(time.RFC3339), tc.reason)
			}
		})
	}
}

// TestEngineParseDivergence documents where the two engines' accepted grammars
// differ, independent of next-fire computation. graith's grammar (via cronx)
// tightens both engines to a single documented contract; this records the raw
// engine differences that motivated pinning it down.
func TestEngineParseDivergence(t *testing.T) {
	cases := []struct {
		expr              string
		robfigOK, gronxOK bool
		note              string
	}{
		{"0 0 * * 7", false, true, "Sunday-as-7: robfig bounds DOW 0-6, gronx allows 7"},
		{"0 0 * * 5-7", false, true, "range ending in 7: robfig rejects, gronx accepts"},
		{"@midnight", true, false, "robfig has @midnight, gronx does not"},
		{"@yearly", true, true, "both engines accept @yearly — but graith's tightened grammar rejects it"},
	}

	for _, tc := range cases {
		_, rErr := diffRobfigParser.Parse(tc.expr)
		robfigOK := rErr == nil
		gronxOK := gronx.IsValid(tc.expr)

		if robfigOK != tc.robfigOK {
			t.Errorf("robfig accept(%q)=%v; want %v (%s)", tc.expr, robfigOK, tc.robfigOK, tc.note)
		}

		if gronxOK != tc.gronxOK {
			t.Errorf("gronx accept(%q)=%v; want %v (%s)", tc.expr, gronxOK, tc.gronxOK, tc.note)
		}

		// Whatever the engines do, graith's own grammar (cronx) must be decisive
		// and documented — every one of these is rejected by cronx.
		if err := Validate(tc.expr); err == nil {
			t.Errorf("cronx unexpectedly accepts %q; the tightened grammar should reject it", tc.expr)
		}
	}
}
