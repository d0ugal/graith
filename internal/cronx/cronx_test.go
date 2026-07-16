package cronx

import (
	"strings"
	"testing"
	"time"
)

// mustLoc loads an IANA location, failing the test if the zone is unavailable
// (some minimal CI images lack the tz database).
func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()

	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("timezone %q unavailable: %v", name, err)
	}

	return loc
}

func TestParseAccepts(t *testing.T) {
	// The documented grammar: five-field forms and the four descriptors.
	good := []string{
		"@hourly", "@daily", "@weekly", "@monthly",
		"@DAILY", " @daily ", // case-insensitive + surrounding whitespace
		"* * * * *",
		"0 9 * * *",
		"*/15 * * * *",
		"0 0 * * 0",   // Sunday as 0
		"0 0 * * SUN", // named Sunday
		"0 0 1,15 * *",
		"0 12 * * 1-5",
		"15,45 8-17 * * MON-FRI",
		"0 0 1 * *",
		"0 0 29 2 *",      // leap day — valid, fires only in leap years
		"0 0 * JAN,DEC 1", // named months
		"23 0-20/2 * * *", // step over a range
		"0 0,12 1 */2 *",  // list + step
	}
	for _, spec := range good {
		if _, err := Parse(spec); err != nil {
			t.Errorf("Parse(%q) = %v; want nil", spec, err)
		}

		if err := Validate(spec); err != nil {
			t.Errorf("Validate(%q) = %v; want nil", spec, err)
		}
	}
}

func TestParseRejects(t *testing.T) {
	// Everything outside the documented grammar, including the undocumented
	// robfig descriptors we deliberately tightened away (issue #1213).
	bad := []struct {
		spec, wantSubstr string
	}{
		{"", "empty"},
		{"   ", "empty"},
		{"not a cron", "5 fields"},
		{"0 9 * *", "5 fields"},        // too few
		{"0 0 9 * * *", "5 fields"},    // six-field (seconds) rejected
		{"0 0 9 * * 2026", "5 fields"}, // seven-ish/year rejected
		{"@yearly", "unsupported descriptor"},
		{"@annually", "unsupported descriptor"},
		{"@midnight", "unsupported descriptor"},
		{"@reboot", "unsupported descriptor"},
		{"@every 5m", "unsupported descriptor"},
		{"@5minutes", "unsupported descriptor"},
		{"0 0 ? * *", "unsupported character"}, // Quartz any-day
		{"0 0 L * *", "unsupported character"}, // last-day
		{"0 0 15W * *", "unsupported character"},
		{"0 0 * * 5#3", "unsupported character"}, // nth weekday
		{"0 0 * * 7", "above maximum"},           // Sunday-as-7 rejected (robfig 0-6)
		{"99 * * * *", "above maximum"},          // out-of-range minute
	}
	for _, tc := range bad {
		_, err := Parse(tc.spec)
		if err == nil {
			t.Errorf("Parse(%q) = nil; want error containing %q", tc.spec, tc.wantSubstr)
			continue
		}

		if !strings.Contains(err.Error(), tc.wantSubstr) {
			t.Errorf("Parse(%q) error = %q; want substring %q", tc.spec, err, tc.wantSubstr)
		}
	}
}

// TestNextCorrectness pins next-fire computation across the cases issue #1213
// calls out: descriptors, wildcards, lists/ranges/steps, named fields, DOM/DOW
// union, and leap days. Expected values are hand-computed and match robfig/cron.
func TestNextCorrectness(t *testing.T) {
	utc := time.UTC

	parse := func(s string) Schedule {
		sched, err := Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q): %v", s, err)
		}

		return sched
	}

	at := func(s string) time.Time {
		ts, err := time.ParseInLocation("2006-01-02T15:04:05", s, utc)
		if err != nil {
			t.Fatalf("bad time %q: %v", s, err)
		}

		return ts
	}

	cases := []struct {
		name, spec, ref, want string
	}{
		{"hourly descriptor", "@hourly", "2026-06-15T10:30:00", "2026-06-15T11:00:00"},
		{"daily descriptor", "@daily", "2026-06-15T10:30:00", "2026-06-16T00:00:00"},
		{"weekly descriptor Sunday", "@weekly", "2026-06-15T10:30:00", "2026-06-21T00:00:00"},
		{"monthly descriptor", "@monthly", "2026-06-15T10:30:00", "2026-07-01T00:00:00"},
		{"every minute", "* * * * *", "2026-06-15T10:30:15", "2026-06-15T10:31:00"},
		{"fixed daily", "0 9 * * *", "2026-06-15T10:30:00", "2026-06-16T09:00:00"},
		{"step minutes", "*/15 * * * *", "2026-06-15T10:31:00", "2026-06-15T10:45:00"},
		{"list of DOM", "0 0 1,15 * *", "2026-06-02T00:00:00", "2026-06-15T00:00:00"},
		{"weekday range", "0 12 * * 1-5", "2026-06-13T00:00:00", "2026-06-15T12:00:00"}, // Sat -> Mon
		{"named weekday Sunday", "0 0 * * SUN", "2026-06-15T00:00:00", "2026-06-21T00:00:00"},
		{"named months", "0 0 1 JAN,DEC *", "2026-06-15T00:00:00", "2026-12-01T00:00:00"},
		{"leap day next-leap", "0 0 29 2 *", "2026-03-01T00:00:00", "2028-02-29T00:00:00"},
		{"day 31 skips short months", "0 0 31 * *", "2026-06-15T00:00:00", "2026-07-31T00:00:00"},
		// DOM/DOW union: fires when EITHER the 13th OR a Friday matches.
		{"DOM-DOW union hits DOW first", "0 0 13 * 5", "2026-06-01T00:00:00", "2026-06-05T00:00:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parse(tc.spec).Next(at(tc.ref))
			if !got.Equal(at(tc.want)) {
				t.Errorf("Next(%q) from %s = %s; want %s", tc.spec, tc.ref, got.Format(time.RFC3339), tc.want)
			}
		})
	}
}

// TestNextRepeatedAcrossYears walks a leap-day schedule forward many times and
// asserts every fire lands on Feb 29 of a leap year — catching drift over long
// horizons (issue #1213: "repeated Next calls across years").
func TestNextRepeatedAcrossYears(t *testing.T) {
	sched, err := Parse("0 0 29 2 *")
	if err != nil {
		t.Fatal(err)
	}

	cur := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		cur = sched.Next(cur)
		if cur.Month() != time.February || cur.Day() != 29 {
			t.Fatalf("iteration %d: got %s; want a Feb-29", i, cur.Format(time.RFC3339))
		}

		if !isLeap(cur.Year()) {
			t.Fatalf("iteration %d: %d is not a leap year", i, cur.Year())
		}
	}
}

func isLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

// TestNextDSTSpringForward: on a spring-forward day the 02:00-02:59 wall-clock
// hour does not exist. A schedule targeting that hour must still advance to a
// real instant rather than hang or return a nonexistent time.
func TestNextDSTSpringForward(t *testing.T) {
	ny := mustLoc(t, "America/New_York")

	// US DST 2026 begins 2026-03-08: 02:00 jumps to 03:00.
	sched, err := Parse("30 2 * * *") // 02:30 daily — skipped on the transition day
	if err != nil {
		t.Fatal(err)
	}

	ref := time.Date(2026, 3, 7, 3, 0, 0, 0, ny) // day before the transition
	got := sched.Next(ref)

	// The next real 02:30 is on 2026-03-09 (02:30 does not exist on the 8th).
	// robfig rolls the skipped instant to the following day; assert we land on a
	// real, DST-correct time that the schedule accepts.
	if got.Hour() != 2 || got.Minute() != 30 {
		t.Fatalf("got %s; want an HH:MM of 02:30", got.Format(time.RFC3339))
	}

	if got.Before(ref) {
		t.Fatalf("Next went backwards: %s < %s", got, ref)
	}

	// Whatever instant we land on must itself be a fire time (idempotent forward).
	if again := sched.Next(got.Add(-time.Second)); !again.Equal(got) {
		t.Errorf("not idempotent near DST gap: %s vs %s", got, again)
	}
}

// TestNextDSTFallBack: on a fall-back day the 01:00-01:59 hour repeats. A
// schedule in that window must still produce a strictly-forward, monotonic
// series (issue #1213: "fall-back repeated times").
func TestNextDSTFallBack(t *testing.T) {
	ny := mustLoc(t, "America/New_York")

	// US DST 2026 ends 2026-11-01: 02:00 falls back to 01:00.
	sched, err := Parse("30 1 * * *") // 01:30 daily
	if err != nil {
		t.Fatal(err)
	}

	ref := time.Date(2026, 10, 31, 12, 0, 0, 0, ny)

	prev := ref
	for i := range 4 {
		next := sched.Next(prev)
		if !next.After(prev) {
			t.Fatalf("iteration %d: Next not strictly forward: %s <= %s", i, next.Format(time.RFC3339), prev.Format(time.RFC3339))
		}

		if next.Hour() != 1 || next.Minute() != 30 {
			t.Fatalf("iteration %d: got %s; want HH:MM 01:30", i, next.Format(time.RFC3339))
		}

		prev = next
	}
}

// TestNextNonHourDST covers a half-hour DST offset (Lord Howe Island shifts by
// 30 minutes), a case standard hour-offset assumptions miss.
func TestNextNonHourDST(t *testing.T) {
	lh := mustLoc(t, "Australia/Lord_Howe")

	sched, err := Parse("0 * * * *") // top of every hour
	if err != nil {
		t.Fatal(err)
	}

	// Lord Howe DST begins 2026-10-04 02:00 -> 02:30 (a 30-minute jump).
	ref := time.Date(2026, 10, 4, 1, 15, 0, 0, lh)

	prev := ref
	for i := range 6 {
		next := sched.Next(prev)
		if !next.After(prev) {
			t.Fatalf("iteration %d: not strictly forward: %s <= %s", i, next, prev)
		}

		if next.Minute() != 0 {
			t.Fatalf("iteration %d: got %s; want minute 0", i, next.Format(time.RFC3339))
		}

		prev = next
	}
}

func TestScheduleString(t *testing.T) {
	sched, err := Parse("0 9 * * *")
	if err != nil {
		t.Fatal(err)
	}

	if sched.String() != "0 9 * * *" {
		t.Errorf("String() = %q; want %q", sched.String(), "0 9 * * *")
	}
}

func TestGrammarDocumented(t *testing.T) {
	// The exported Grammar string is referenced by docs; guard the descriptors
	// stay listed so a future edit can't silently drop one.
	for d := range allowedDescriptors {
		if !strings.Contains(Grammar, d) {
			t.Errorf("Grammar is missing descriptor %q", d)
		}
	}
}
