// Package timeutil holds the IST helpers. "Today" everywhere in Stocky means
// the IST calendar day, regardless of the machine's timezone.
package timeutil

import (
	"time"
)

// IST is Asia/Kolkata. It is loaded once by Init at startup; the binary
// embeds the zone database (time/tzdata), so loading cannot depend on the
// machine having zone files.
var IST *time.Location

// DateLayout is the format of an IST calendar date, e.g. 2026-09-26.
const DateLayout = "2006-01-02"

// Init loads Asia/Kolkata into IST. Call it before anything else.
func Init() error {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return err
	}
	IST = loc
	return nil
}

// StartOfDayIST returns midnight IST at the start of t's IST calendar day.
func StartOfDayIST(t time.Time) time.Time {
	y, m, d := t.In(IST).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, IST)
}

// StartOfNextDayIST returns midnight IST at the start of the following day.
// A day is the half-open window [StartOfDayIST(t), StartOfNextDayIST(t)).
func StartOfNextDayIST(t time.Time) time.Time {
	// AddDate on the calendar date, not Add(24h): the result is always the
	// next midnight even if a zone had a DST change (IST has none today).
	return StartOfDayIST(t).AddDate(0, 0, 1)
}

// ISTDate returns t's IST calendar date as "YYYY-MM-DD".
func ISTDate(t time.Time) string {
	return t.In(IST).Format(DateLayout)
}

// ParseISTDate parses "YYYY-MM-DD" as midnight IST on that date.
func ParseISTDate(s string) (time.Time, error) {
	return time.ParseInLocation(DateLayout, s, IST)
}

// Format renders t as RFC3339 in IST, e.g. 2026-09-26T10:31:00+05:30.
// Every timestamp in an API response goes through here.
func Format(t time.Time) string {
	return t.In(IST).Format(time.RFC3339)
}
