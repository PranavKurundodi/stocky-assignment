package timeutil

import (
	"os"
	"testing"
	"time"
	_ "time/tzdata"
)

func TestMain(m *testing.M) {
	if err := Init(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestEarlyMorningISTBelongsToThatISTDay(t *testing.T) {
	// 00:30 IST on Sep 26 is 19:00 UTC on Sep 25. A UTC-based "today"
	// would put it on the 25th; IST must put it on the 26th.
	ts := time.Date(2026, 9, 25, 19, 0, 0, 0, time.UTC)

	if got := ISTDate(ts); got != "2026-09-26" {
		t.Errorf("ISTDate = %s, want 2026-09-26", got)
	}
	wantStart := time.Date(2026, 9, 26, 0, 0, 0, 0, IST)
	if got := StartOfDayIST(ts); !got.Equal(wantStart) {
		t.Errorf("StartOfDayIST = %v, want %v", got, wantStart)
	}
}

func TestDayWindowIsHalfOpen(t *testing.T) {
	day := time.Date(2026, 9, 26, 12, 0, 0, 0, IST)
	start, end := StartOfDayIST(day), StartOfNextDayIST(day)

	if !end.Equal(time.Date(2026, 9, 27, 0, 0, 0, 0, IST)) {
		t.Fatalf("StartOfNextDayIST = %v", end)
	}

	inWindow := func(ts time.Time) bool { return !ts.Before(start) && ts.Before(end) }

	cases := []struct {
		ts   time.Time
		want bool
	}{
		{start, true},                     // midnight belongs to the day it starts
		{end.Add(-time.Nanosecond), true}, // last instant of the day
		{end, false},                      // next midnight belongs to the next day
		{start.Add(-time.Nanosecond), false},
	}
	for _, tc := range cases {
		if got := inWindow(tc.ts); got != tc.want {
			t.Errorf("inWindow(%v) = %v, want %v", tc.ts, got, tc.want)
		}
		// ISTDate must agree with the window.
		if got := ISTDate(tc.ts) == "2026-09-26"; got != tc.want {
			t.Errorf("ISTDate(%v) = %s, disagrees with window", tc.ts, ISTDate(tc.ts))
		}
	}
}

func TestFormatUsesISTOffset(t *testing.T) {
	ts := time.Date(2026, 9, 26, 5, 1, 0, 0, time.UTC)
	if got := Format(ts); got != "2026-09-26T10:31:00+05:30" {
		t.Errorf("Format = %s", got)
	}
}

func TestParseISTDate(t *testing.T) {
	got, err := ParseISTDate("2026-09-26")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(time.Date(2026, 9, 25, 18, 30, 0, 0, time.UTC)) {
		t.Errorf("ParseISTDate = %v", got)
	}
}
