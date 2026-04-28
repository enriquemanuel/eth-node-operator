package maintenance_test

import (
	"testing"
	"time"

	"github.com/enriquemanuel/eth-node-operator/internal/maintenance"
)

// Sunday 2024-01-07 02:00 UTC
var sunday2am = time.Date(2024, 1, 7, 2, 0, 0, 0, time.UTC)

func TestWindowIsOpen_ExactTime(t *testing.T) {
	w, err := maintenance.New("0 2 * * 0") // Sunday 2am
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !w.IsOpen(sunday2am) {
		t.Error("window should be open at Sunday 2:00")
	}
}

func TestWindowIsOpen_WithinHour(t *testing.T) {
	w, err := maintenance.New("0 2 * * 0")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 2:30am still in the window
	at230 := sunday2am.Add(30 * time.Minute)
	if !w.IsOpen(at230) {
		t.Error("window should be open at Sunday 2:30")
	}
}

func TestWindowIsOpen_WrongDay(t *testing.T) {
	w, err := maintenance.New("0 2 * * 0") // Sunday only
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Monday 2am
	monday2am := sunday2am.Add(24 * time.Hour)
	if w.IsOpen(monday2am) {
		t.Error("window should be closed on Monday")
	}
}

func TestWindowIsOpen_WrongHour(t *testing.T) {
	w, err := maintenance.New("0 2 * * 0")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Sunday 3am
	at3am := sunday2am.Add(time.Hour)
	if w.IsOpen(at3am) {
		t.Error("window should be closed at 3am")
	}
}

func TestWindowIsOpen_BeforeScheduledMinute(t *testing.T) {
	w, err := maintenance.New("30 2 * * 0") // 2:30am
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 2:15am — before the scheduled minute
	at215 := sunday2am.Add(15 * time.Minute)
	if w.IsOpen(at215) {
		t.Error("window should be closed before scheduled minute")
	}
}

func TestWindowNextOpen_FindsNextSunday(t *testing.T) {
	w, err := maintenance.New("0 2 * * 0") // Sunday 2am
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Start from Monday
	from := sunday2am.Add(24 * time.Hour)
	next := w.NextOpen(from)

	if next.IsZero() {
		t.Fatal("expected a valid next open time")
	}
	if next.Weekday() != time.Sunday {
		t.Errorf("expected Sunday, got %s", next.Weekday())
	}
	if next.Hour() != 2 {
		t.Errorf("expected hour 2, got %d", next.Hour())
	}
}

func TestWindowNextOpen_SameDay(t *testing.T) {
	w, err := maintenance.New("0 2 * * 0")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// 1am Sunday — next open is 2am same day
	from := sunday2am.Add(-time.Hour)
	next := w.NextOpen(from)

	if next.IsZero() {
		t.Fatal("expected a valid next open time")
	}
	if next.Hour() != 2 {
		t.Errorf("expected next open at hour 2, got %d", next.Hour())
	}
}

func TestWindowParse_InvalidCron(t *testing.T) {
	cases := []string{
		"",
		"not a cron",
		"0 2 * *",     // only 4 fields
		"0 25 * * 0",  // invalid hour
		"70 2 * * 0",  // invalid minute
	}
	for _, expr := range cases {
		_, err := maintenance.New(expr)
		if err == nil {
			t.Errorf("expected error for cron %q", expr)
		}
	}
}

func TestWindowSchedule(t *testing.T) {
	expr := "0 3 * * 6"
	w, err := maintenance.New(expr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if w.Schedule() != expr {
		t.Errorf("expected %q, got %q", expr, w.Schedule())
	}
}
