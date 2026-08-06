package server

import (
	"testing"
	"time"

	"github.com/simons-agent-space/cron-dashboard/internal/domain"
)

func TestFmtScheduleHuman(t *testing.T) {
	tests := []struct {
		name string
		job  domain.Job
		want string
	}{
		// Standard 5-field cron: simple patterns.
		{"cron every minute", domain.Job{ScheduleKind: "cron", ScheduleExpr: "* * * * *"}, "Every minute"},
		{"cron every 5 minutes", domain.Job{ScheduleKind: "cron", ScheduleExpr: "*/5 * * * *"}, "Every 5 minutes"},
		{"cron every hour", domain.Job{ScheduleKind: "cron", ScheduleExpr: "0 * * * *"}, "Every hour"},
		{"cron every 2 hours", domain.Job{ScheduleKind: "cron", ScheduleExpr: "0 */2 * * *"}, "At 0 minutes past the hour, every 2 hours"},

		// Day / month / weekday specifics.
		{"cron every day at 09:00", domain.Job{ScheduleKind: "cron", ScheduleExpr: "0 9 * * *"}, "At 09:00"},
		{"cron Monday at 06:00", domain.Job{ScheduleKind: "cron", ScheduleExpr: "0 6 * * 1"}, "At 06:00, only on Monday"},
		{"cron Sunday 0", domain.Job{ScheduleKind: "cron", ScheduleExpr: "0 9 * * 0"}, "At 09:00, only on Sunday"},
		{"cron Sunday 7 also Sunday", domain.Job{ScheduleKind: "cron", ScheduleExpr: "0 9 * * 7"}, "At 09:00, only on Sunday"},
		{"cron day of month", domain.Job{ScheduleKind: "cron", ScheduleExpr: "0 6 1 * *"}, "At 06:00, on day 1 of the month"},
		{"cron day of month with non-zero minute", domain.Job{ScheduleKind: "cron", ScheduleExpr: "30 14 15 * *"}, "At 14:30, on day 15 of the month"},

		// Ranges and lists — these used to return "" with the hand-rolled
		// matcher and now produce a description via lnquy/cron.
		{"cron weekday range", domain.Job{ScheduleKind: "cron", ScheduleExpr: "0 9 * * 1-5"}, "At 09:00, Monday through Friday"},
		{"cron dom list", domain.Job{ScheduleKind: "cron", ScheduleExpr: "0 9 1,15 * *"}, "At 09:00, on day 1 and 15 of the month"},

		// Unparseable: error + empty → caller hides the line.
		{"cron out of range returns empty", domain.Job{ScheduleKind: "cron", ScheduleExpr: "99 99 * * *"}, ""},
		{"cron empty expr returns empty", domain.Job{ScheduleKind: "cron", ScheduleExpr: ""}, ""},

		// every duration humanization.
		{"every 30m", domain.Job{ScheduleKind: "every", EveryMS: 30 * 60 * 1000}, "Every 30m"},
		{"every 1h", domain.Job{ScheduleKind: "every", EveryMS: 60 * 60 * 1000}, "Every 1h"},
		{"every zero returns empty", domain.Job{ScheduleKind: "every", EveryMS: 0}, ""},

		// at / unknown kinds: no humanization.
		{"at returns empty", domain.Job{ScheduleKind: "at", At: "2026-08-07T08:30:00Z"}, ""},
		{"unknown kind returns empty", domain.Job{ScheduleKind: "unknown"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fmtScheduleHuman(tt.job)
			if got != tt.want {
				t.Errorf("fmtScheduleHuman(%+v) = %q, want %q", tt.job, got, tt.want)
			}
		})
	}
}

func TestHumanizeDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "0ms"},
		{"negative", -5 * time.Second, "0ms"},
		{"sub-second", 250 * time.Millisecond, "250ms"},
		{"whole seconds", 5 * time.Second, "5s"},
		{"whole minutes", 30 * time.Minute, "30m"},
		{"minutes and seconds", 90 * time.Second, "1m 30s"},
		{"whole hours", 2 * time.Hour, "2h"},
		{"hours and minutes", 2*time.Hour + 15*time.Minute, "2h 15m"},
		{"whole days", 48 * time.Hour, "2d"},
		{"days and hours", 50 * time.Hour, "2d 2h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := humanizeDuration(tt.d)
			if got != tt.want {
				t.Errorf("humanizeDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
