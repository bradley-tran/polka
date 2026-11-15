package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronScheduleSearchYears bounds how far Next looks ahead. February 29 is the
// rarest date ParseCronSchedule accepts, so five years covers every ordinary
// schedule. Only contrived combinations, such as a February 29 that must also
// be a Sunday, can come back empty.
const cronScheduleSearchYears = 5

// cronMacros maps the supported @-shortcuts to their five-field equivalents.
var cronMacros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// cronMonthNames and cronWeekdayNames are the case-insensitive aliases cron
// accepts in the month and day-of-week fields.
var (
	cronMonthNames = map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}
	cronWeekdayNames = map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}
)

// cronField describes the legal range and aliases of one schedule field.
type cronField struct {
	name  string
	min   int
	max   int
	names map[string]int
}

// cronFields lists the five schedule fields in expression order. Day of week
// accepts 0-7 so both 0 and 7 mean Sunday; bit 7 is folded into bit 0 after
// parsing.
var cronFields = [5]cronField{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: cronMonthNames},
	{name: "day of week", min: 0, max: 7, names: cronWeekdayNames},
}

// CronSchedule is a parsed five-field cron expression. Each field is a bitset
// of the values it matches. Matching follows Vixie cron: when both day of
// month and day of week are restricted, a day matches if either does.
type CronSchedule struct {
	expression string
	minute     uint64
	hour       uint64
	dayOfMonth uint64
	month      uint64
	dayOfWeek  uint64
	// domStar and dowStar record whether the day fields start with "*", which
	// is how Vixie cron decides between AND and OR day matching.
	domStar bool
	dowStar bool
}

// ParseCronSchedule parses a standard five-field cron expression (minute,
// hour, day of month, month, day of week) or one of the @yearly, @annually,
// @monthly, @weekly, @daily, @midnight, and @hourly macros. Fields accept "*",
// single values, ranges ("1-5"), steps ("*/15", "0-30/10", "5/10"), comma
// lists, and month or weekday names. Schedules that can never run, such as
// February 30, are rejected.
func ParseCronSchedule(expression string) (CronSchedule, error) {
	trimmed := strings.Join(strings.Fields(expression), " ")
	if trimmed == "" {
		return CronSchedule{}, fmt.Errorf("cron schedule must not be empty")
	}

	fieldsSource := trimmed
	if strings.HasPrefix(trimmed, "@") {
		macro, ok := cronMacros[strings.ToLower(trimmed)]
		if !ok {
			return CronSchedule{}, fmt.Errorf("unsupported cron schedule macro %q", trimmed)
		}
		fieldsSource = macro
	}

	parts := strings.Fields(fieldsSource)
	if len(parts) != len(cronFields) {
		return CronSchedule{}, fmt.Errorf("cron schedule %q must have 5 fields (minute hour day-of-month month day-of-week), got %d", trimmed, len(parts))
	}

	var bits [5]uint64
	for index, part := range parts {
		value, err := parseCronField(part, cronFields[index])
		if err != nil {
			return CronSchedule{}, fmt.Errorf("cron schedule %q: %w", trimmed, err)
		}
		bits[index] = value
	}
	// Sunday may be written as 0 or 7; fold 7 into 0 so matching only checks
	// time.Weekday values.
	if bits[4]&(1<<7) != 0 {
		bits[4] = (bits[4] &^ (1 << 7)) | 1
	}

	schedule := CronSchedule{
		expression: trimmed,
		minute:     bits[0],
		hour:       bits[1],
		dayOfMonth: bits[2],
		month:      bits[3],
		dayOfWeek:  bits[4],
		domStar:    strings.HasPrefix(parts[2], "*"),
		dowStar:    strings.HasPrefix(parts[4], "*"),
	}
	if !schedule.canRun() {
		return CronSchedule{}, fmt.Errorf("cron schedule %q never runs: no selected month has the selected day of month", trimmed)
	}

	return schedule, nil
}

// String returns the expression the schedule was parsed from, with runs of
// whitespace collapsed.
func (s CronSchedule) String() string {
	return s.expression
}

// Matches reports whether the schedule fires during the minute containing t,
// evaluated in t's location.
func (s CronSchedule) Matches(t time.Time) bool {
	if s.minute&(1<<uint(t.Minute())) == 0 {
		return false
	}
	if s.hour&(1<<uint(t.Hour())) == 0 {
		return false
	}
	if s.month&(1<<uint(t.Month())) == 0 {
		return false
	}

	return s.matchesDay(t)
}

// Next returns the start of the first minute strictly after the minute
// containing after in which the schedule fires, in after's location. It
// returns the zero time for a zero-value schedule that never matches.
func (s CronSchedule) Next(after time.Time) time.Time {
	location := after.Location()
	candidate := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), after.Minute(), 0, 0, location).Add(time.Minute)
	limit := candidate.AddDate(cronScheduleSearchYears, 0, 0)

	// Skip whole months, days, and hours that cannot match instead of testing
	// every minute, so even yearly schedules resolve in a few hundred steps.
	for candidate.Before(limit) {
		if s.month&(1<<uint(candidate.Month())) == 0 {
			candidate = time.Date(candidate.Year(), candidate.Month()+1, 1, 0, 0, 0, 0, location)
			continue
		}
		if !s.matchesDay(candidate) {
			candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day()+1, 0, 0, 0, 0, location)
			continue
		}
		if s.hour&(1<<uint(candidate.Hour())) == 0 {
			candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day(), candidate.Hour()+1, 0, 0, 0, location)
			continue
		}
		if s.minute&(1<<uint(candidate.Minute())) == 0 {
			candidate = candidate.Add(time.Minute)
			continue
		}

		return candidate
	}

	return time.Time{}
}

// matchesDay applies Vixie cron's day rule: if either day field is
// unrestricted ("*"-prefixed), both must match; otherwise either may match.
func (s CronSchedule) matchesDay(t time.Time) bool {
	domMatch := s.dayOfMonth&(1<<uint(t.Day())) != 0
	dowMatch := s.dayOfWeek&(1<<uint(t.Weekday())) != 0
	if s.domStar || s.dowStar {
		return domMatch && dowMatch
	}

	return domMatch || dowMatch
}

// canRun reports whether some selected month contains some selected day of
// month. Day-of-week OR matching always finds a day, so the check only applies
// when the day of month must match on its own.
func (s CronSchedule) canRun() bool {
	if s.minute == 0 || s.hour == 0 || s.month == 0 || s.dayOfMonth == 0 || s.dayOfWeek == 0 {
		return false
	}
	if !s.domStar && !s.dowStar {
		return true
	}
	// February counts as 29 days because leap years make the 29th reachable.
	daysInMonth := [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	for month := 1; month <= 12; month++ {
		if s.month&(1<<uint(month)) == 0 {
			continue
		}
		for day := 1; day <= daysInMonth[month]; day++ {
			if s.dayOfMonth&(1<<uint(day)) != 0 {
				return true
			}
		}
	}

	return false
}

// parseCronField parses one comma-separated field into a bitset of matched
// values.
func parseCronField(source string, field cronField) (uint64, error) {
	var bits uint64
	for _, item := range strings.Split(source, ",") {
		if item == "" {
			return 0, fmt.Errorf("%s field %q has an empty list item", field.name, source)
		}
		itemBits, err := parseCronFieldItem(item, field)
		if err != nil {
			return 0, err
		}
		bits |= itemBits
	}

	return bits, nil
}

// parseCronFieldItem parses one list item: "*", a value, or a range, each
// optionally followed by "/step". A bare value with a step ("5/10") runs from
// that value to the field maximum.
func parseCronFieldItem(item string, field cronField) (uint64, error) {
	base, stepSource, hasStep := strings.Cut(item, "/")
	step := 1
	if hasStep {
		parsed, err := strconv.Atoi(stepSource)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("%s field %q has an invalid step %q", field.name, item, stepSource)
		}
		step = parsed
	}

	var low, high int
	switch {
	case base == "*":
		low, high = field.min, field.max
	case strings.Contains(base, "-"):
		lowSource, highSource, _ := strings.Cut(base, "-")
		var err error
		if low, err = parseCronValue(lowSource, field); err != nil {
			return 0, err
		}
		if high, err = parseCronValue(highSource, field); err != nil {
			return 0, err
		}
		if low > high {
			return 0, fmt.Errorf("%s field range %q starts after it ends", field.name, base)
		}
	default:
		value, err := parseCronValue(base, field)
		if err != nil {
			return 0, err
		}
		low, high = value, value
		if hasStep {
			high = field.max
		}
	}

	var bits uint64
	for value := low; value <= high; value += step {
		bits |= 1 << uint(value)
	}

	return bits, nil
}

// parseCronValue parses a number or, where the field allows, a three-letter
// month or weekday name, and checks it against the field's range.
func parseCronValue(source string, field cronField) (int, error) {
	if value, ok := field.names[strings.ToLower(source)]; ok {
		return value, nil
	}
	value, err := strconv.Atoi(source)
	if err != nil {
		return 0, fmt.Errorf("%s field has an invalid value %q", field.name, source)
	}
	if value < field.min || value > field.max {
		return 0, fmt.Errorf("%s field value %d is outside %d-%d", field.name, value, field.min, field.max)
	}

	return value, nil
}
