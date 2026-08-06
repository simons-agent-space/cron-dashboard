package server

import (
	"fmt"
	"time"

	_ "time/tzdata" // embed the IANA tz database so distroless can format Berlin times

	"github.com/lnquy/cron"
	"github.com/simons-agent-space/cron-dashboard/internal/domain"
)

var berlin, _ = time.LoadLocation("Europe/Berlin")

// humanCronDesc turns cron expressions into English for the dashboard's schedule line.
var humanCronDesc = func() *cron.ExpressionDescriptor {
	d, err := cron.NewDescriptor(cron.Use24HourTimeFormat(true))
	if err != nil {
		panic("cron: NewDescriptor: " + err.Error())
	}
	return d
}()

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

// fmtScheduleHuman renders a plain-English sentence for the second schedule line.
// Returns "" when no humanization applies (the template's {{with}} block hides the line).
func fmtScheduleHuman(j domain.Job) string {
	switch j.ScheduleKind {
	case "cron":
		desc, err := humanCronDesc.ToDescription(j.ScheduleExpr, cron.Locale_en)
		if err != nil {
			return ""
		}
		return desc
	case "every":
		if j.EveryMS <= 0 {
			return ""
		}
		return "Every " + humanizeDuration(time.Duration(j.EveryMS)*time.Millisecond)
	default:
		return ""
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
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	if d < time.Hour {
		minutes := int(d / time.Minute)
		seconds := int(d/time.Second) % 60
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	if d < 24*time.Hour {
		hours := int(d / time.Hour)
		minutes := int(d/time.Minute) % 60
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	if hours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, hours)
}
