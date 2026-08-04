package server

import (
	"fmt"
	"time"

	_ "time/tzdata" // embed the IANA tz database so distroless can format Berlin times

	"github.com/simons-agent-space/crons/internal/domain"
)

// berlin is the canonical render timezone. Loaded once at startup; the
// embedded tzdata above ensures this works in a distroless runtime.
var berlin, _ = time.LoadLocation("Europe/Berlin")

// fmtTime renders a time.Time in Europe/Berlin. The zero value renders as
// an em dash so missing timestamps do not look like 1970-01-01.
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(berlin).Format("2006-01-02 15:04:05 MST")
}

// fmtTimeOpt renders a *time.Time in Europe/Berlin. nil renders as "—".
func fmtTimeOpt(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "—"
	}
	return fmtTime(*t)
}

// fmtDurationMS renders a duration in milliseconds as a short humanized
// string. Zero or negative renders as "—".
func fmtDurationMS(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return humanizeDuration(time.Duration(ms) * time.Millisecond)
}

// fmtSchedule renders a job's schedule in a single human-readable string.
func fmtSchedule(j domain.Job) string {
	switch j.ScheduleKind {
	case "cron":
		expr := j.ScheduleExpr
		if expr == "" {
			expr = "(no expression)"
		}
		if j.ScheduleTZ != "" {
			return fmt.Sprintf("cron %s (%s)", expr, j.ScheduleTZ)
		}
		return fmt.Sprintf("cron %s", expr)
	case "every":
		if j.EveryMS <= 0 {
			return "every ?"
		}
		return "every " + humanizeDuration(time.Duration(j.EveryMS)*time.Millisecond)
	case "at":
		if j.At == "" {
			return "at (no value)"
		}
		return "at " + j.At
	default:
		if j.ScheduleKind == "" {
			return "—"
		}
		return j.ScheduleKind
	}
}

// humanizeDuration picks the largest unit that yields a value >= 1 and
// prints only the non-zero components. Sub-second durations are rendered
// as "Nms".
func humanizeDuration(d time.Duration) string {
	if d <= 0 {
		return "0ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := int(d.Seconds()) % 60
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	if hours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, hours)
}
