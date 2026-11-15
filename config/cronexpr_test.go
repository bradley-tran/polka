package config

import (
	"strings"
	"testing"
	"time"
)

// cronTime builds a UTC minute for schedule tests.
func cronTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02 15:04", value)
	if err != nil {
		t.Fatalf("time.Parse(%q) error = %v", value, err)
	}

	return parsed
}

// TestParseCronScheduleMatches checks every field form against minutes that
// should and should not match.
func TestParseCronScheduleMatches(t *testing.T) {
	for _, test := range []struct {
		name       string
		expression string
		matches    []string
		misses     []string
	}{
		{name: "every minute", expression: "* * * * *", matches: []string{"2026-11-15 00:00", "2026-11-15 13:37"}},
		{name: "single values", expression: "30 2 * * *", matches: []string{"2026-11-15 02:30"}, misses: []string{"2026-11-15 02:31", "2026-11-15 03:30"}},
		{name: "minute step", expression: "*/15 * * * *", matches: []string{"2026-11-15 10:00", "2026-11-15 10:45"}, misses: []string{"2026-11-15 10:10"}},
		{name: "range step", expression: "0-30/10 * * * *", matches: []string{"2026-11-15 10:30"}, misses: []string{"2026-11-15 10:40"}},
		{name: "value step runs to max", expression: "5/20 * * * *", matches: []string{"2026-11-15 10:05", "2026-11-15 10:45"}, misses: []string{"2026-11-15 10:00"}},
		{name: "list", expression: "0 8,12,18 * * *", matches: []string{"2026-11-15 12:00"}, misses: []string{"2026-11-15 13:00"}},
		{name: "range", expression: "0 9-17 * * *", matches: []string{"2026-11-15 17:00"}, misses: []string{"2026-11-15 18:00"}},
		{name: "month names", expression: "0 0 1 jan,JUL *", matches: []string{"2026-07-01 00:00"}, misses: []string{"2026-08-01 00:00"}},
		// 2026-11-15 is a Sunday and 2026-11-16 a Monday.
		{name: "weekday names", expression: "0 9 * * MON-FRI", matches: []string{"2026-11-16 09:00"}, misses: []string{"2026-11-15 09:00"}},
		{name: "sunday as 7", expression: "0 0 * * 7", matches: []string{"2026-11-15 00:00"}, misses: []string{"2026-11-16 00:00"}},
		{name: "weekday range through 7", expression: "0 0 * * 5-7", matches: []string{"2026-11-15 00:00", "2026-11-14 00:00"}, misses: []string{"2026-11-16 00:00"}},
		// Both day fields restricted: either may match (Vixie OR rule).
		{name: "day OR weekday", expression: "0 0 1 * MON", matches: []string{"2026-11-01 00:00", "2026-11-16 00:00"}, misses: []string{"2026-11-15 00:00"}},
		// A "*"-prefixed weekday step keeps AND semantics.
		{name: "day AND starred weekday", expression: "0 0 15 * */7", matches: []string{"2026-11-15 00:00"}, misses: []string{"2026-11-22 00:00", "2026-12-15 00:00"}},
		{name: "hourly macro", expression: "@hourly", matches: []string{"2026-11-15 05:00"}, misses: []string{"2026-11-15 05:01"}},
		{name: "daily macro", expression: "@daily", matches: []string{"2026-11-15 00:00"}, misses: []string{"2026-11-15 01:00"}},
		{name: "midnight macro", expression: "@midnight", matches: []string{"2026-11-15 00:00"}},
		{name: "weekly macro", expression: "@weekly", matches: []string{"2026-11-15 00:00"}, misses: []string{"2026-11-16 00:00"}},
		{name: "monthly macro", expression: "@monthly", matches: []string{"2026-12-01 00:00"}, misses: []string{"2026-12-02 00:00"}},
		{name: "yearly macro", expression: "@YEARLY", matches: []string{"2027-01-01 00:00"}, misses: []string{"2027-02-01 00:00"}},
		{name: "annually macro", expression: "@annually", matches: []string{"2027-01-01 00:00"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			schedule, err := ParseCronSchedule(test.expression)
			if err != nil {
				t.Fatalf("ParseCronSchedule(%q) error = %v", test.expression, err)
			}
			for _, value := range test.matches {
				if !schedule.Matches(cronTime(t, value)) {
					t.Errorf("%q should match %s", test.expression, value)
				}
			}
			for _, value := range test.misses {
				if schedule.Matches(cronTime(t, value)) {
					t.Errorf("%q should not match %s", test.expression, value)
				}
			}
		})
	}
}

// TestParseCronScheduleRejectsInvalid checks the error for each malformed or
// impossible expression.
func TestParseCronScheduleRejectsInvalid(t *testing.T) {
	for _, test := range []struct {
		expression string
		wantErr    string
	}{
		{expression: "", wantErr: "must not be empty"},
		{expression: "* * * *", wantErr: "must have 5 fields"},
		{expression: "* * * * * *", wantErr: "must have 5 fields"},
		{expression: "@reboot", wantErr: "unsupported cron schedule macro"},
		{expression: "60 * * * *", wantErr: "outside 0-59"},
		{expression: "* 24 * * *", wantErr: "outside 0-23"},
		{expression: "* * 0 * *", wantErr: "outside 1-31"},
		{expression: "* * * 13 *", wantErr: "outside 1-12"},
		{expression: "* * * * 8", wantErr: "outside 0-7"},
		{expression: "*/0 * * * *", wantErr: "invalid step"},
		{expression: "*/x * * * *", wantErr: "invalid step"},
		{expression: "10-5 * * * *", wantErr: "starts after it ends"},
		{expression: "1,,2 * * * *", wantErr: "empty list item"},
		{expression: "* * * foo *", wantErr: "invalid value"},
		{expression: "0 0 30 2 *", wantErr: "never runs"},
		{expression: "0 0 31 4,6 *", wantErr: "never runs"},
	} {
		t.Run(test.expression, func(t *testing.T) {
			_, err := ParseCronSchedule(test.expression)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ParseCronSchedule(%q) error = %v, want %q", test.expression, err, test.wantErr)
			}
		})
	}
}

// TestCronScheduleNext checks the next run across minute, day, month, year,
// and leap-day boundaries.
func TestCronScheduleNext(t *testing.T) {
	for _, test := range []struct {
		expression string
		after      string
		want       string
	}{
		{expression: "* * * * *", after: "2026-11-15 10:00", want: "2026-11-15 10:01"},
		{expression: "*/15 * * * *", after: "2026-11-15 10:14", want: "2026-11-15 10:15"},
		{expression: "*/15 * * * *", after: "2026-11-15 10:15", want: "2026-11-15 10:30"},
		{expression: "0 * * * *", after: "2026-11-15 23:30", want: "2026-11-16 00:00"},
		{expression: "@monthly", after: "2026-11-15 10:00", want: "2026-12-01 00:00"},
		{expression: "@yearly", after: "2026-11-15 10:00", want: "2027-01-01 00:00"},
		{expression: "0 0 31 * *", after: "2026-11-01 00:00", want: "2026-12-31 00:00"},
		{expression: "0 12 29 2 *", after: "2026-03-01 00:00", want: "2028-02-29 12:00"},
		{expression: "0 9 * * MON", after: "2026-11-15 10:00", want: "2026-11-16 09:00"},
	} {
		t.Run(test.expression+" after "+test.after, func(t *testing.T) {
			schedule, err := ParseCronSchedule(test.expression)
			if err != nil {
				t.Fatalf("ParseCronSchedule(%q) error = %v", test.expression, err)
			}
			got := schedule.Next(cronTime(t, test.after))
			if want := cronTime(t, test.want); !got.Equal(want) {
				t.Fatalf("Next(%s) = %s, want %s", test.after, got.Format("2006-01-02 15:04"), test.want)
			}
		})
	}
}

// TestCronScheduleNextIgnoresSeconds checks that a time partway through a
// minute still advances to the following matching minute.
func TestCronScheduleNextIgnoresSeconds(t *testing.T) {
	schedule, err := ParseCronSchedule("* * * * *")
	if err != nil {
		t.Fatalf("ParseCronSchedule() error = %v", err)
	}
	after := time.Date(2026, 11, 15, 10, 0, 59, 999, time.UTC)
	if got, want := schedule.Next(after), time.Date(2026, 11, 15, 10, 1, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("Next(%s) = %s, want %s", after, got, want)
	}
}

// TestCronScheduleString checks that the parsed expression is kept with
// whitespace collapsed.
func TestCronScheduleString(t *testing.T) {
	schedule, err := ParseCronSchedule("  */5   *  * * * ")
	if err != nil {
		t.Fatalf("ParseCronSchedule() error = %v", err)
	}
	if got := schedule.String(); got != "*/5 * * * *" {
		t.Fatalf("String() = %q, want %q", got, "*/5 * * * *")
	}
}
