package maintenance

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Window represents a cron-based maintenance schedule.
type Window struct {
	schedule string
	parsed   cronSchedule
}

type cronSchedule struct {
	minute  int
	hour    int
	weekday int // -1 = any
}

// New parses a cron expression and returns a Window.
// Supports 5-field cron: "0 2 * * 0" (minute hour dom month dow)
// Wildcards (*) are supported for dom and month.
func New(cronExpr string) (*Window, error) {
	parsed, err := parseCron(cronExpr)
	if err != nil {
		return nil, fmt.Errorf("parse cron %q: %w", cronExpr, err)
	}
	return &Window{schedule: cronExpr, parsed: parsed}, nil
}

// IsOpen returns true if now falls within the maintenance window (1 hour wide).
func (w *Window) IsOpen(now time.Time) bool {
	if w.parsed.weekday >= 0 && int(now.Weekday()) != w.parsed.weekday {
		return false
	}
	if now.Hour() != w.parsed.hour {
		return false
	}
	if now.Minute() < w.parsed.minute {
		return false
	}
	return true
}

// NextOpen returns the next time the window opens after 'from'.
func (w *Window) NextOpen(from time.Time) time.Time {
	t := from.Truncate(time.Minute)
	for i := 0; i < 10080; i++ {
		t = t.Add(time.Minute)
		if w.IsOpen(t) {
			return t
		}
	}
	return time.Time{}
}

// Schedule returns the original cron expression.
func (w *Window) Schedule() string {
	return w.schedule
}

func parseCron(expr string) (cronSchedule, error) {
	expr = strings.TrimSpace(expr)
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cronSchedule{}, fmt.Errorf("expected 5 fields, got %d", len(fields))
	}

	min, err := parseField(fields[0], 0, 59)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("invalid minute %q: %w", fields[0], err)
	}
	hour, err := parseField(fields[1], 0, 23)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("invalid hour %q: %w", fields[1], err)
	}
	// fields[2] = dom, fields[3] = month — we treat these as wildcards for now
	dow := -1
	if fields[4] != "*" {
		v, err := parseField(fields[4], 0, 7)
		if err != nil {
			return cronSchedule{}, fmt.Errorf("invalid dow %q: %w", fields[4], err)
		}
		if v == 7 {
			v = 0 // Sunday can be 0 or 7
		}
		dow = v
	}

	return cronSchedule{minute: min, hour: hour, weekday: dow}, nil
}

func parseField(s string, min, max int) (int, error) {
	if s == "*" {
		return min, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("not a number: %q", s)
	}
	if v < min || v > max {
		return 0, fmt.Errorf("value %d out of range [%d, %d]", v, min, max)
	}
	return v, nil
}
