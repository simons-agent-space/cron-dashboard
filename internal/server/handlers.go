package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/simons-agent-space/cron-dashboard/internal/domain"
)

// indexData is the data the index template renders.
type indexData struct {
	Jobs   []domain.Job
	Q      string
	Status string
	Sort   string
	Dir    string
}

// SortHeader returns the href for a sortable column header, preserving
// the current query, status, and toggling dir if this axis is already
// the active sort.
func (d indexData) SortHeader(axis string) string {
	dir := "asc"
	if d.Sort == axis && d.Dir == "asc" {
		dir = "desc"
	}
	v := url.Values{}
	if d.Q != "" {
		v.Set("q", d.Q)
	}
	if d.Status != "" {
		v.Set("status", d.Status)
	}
	v.Set("sort", axis)
	v.Set("dir", dir)
	return "/?" + v.Encode()
}

// SortIndicator returns an arrow glyph when axis is the active sort
// column, "" otherwise.
func (d indexData) SortIndicator(axis string) string {
	if d.Sort != axis {
		return ""
	}
	if d.Dir == "desc" {
		return " ↓"
	}
	return " ↑"
}

// jobData is the data the job detail template renders.
type jobData struct {
	Job     *domain.Job
	RunLogs []domain.RunLog
}

// handleHealthz returns a small JSON payload: "ok" with HTTP 200 when the
// database can be queried and HTTP 503 otherwise. The body is always JSON
// because container and external probes expect a parseable response.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if err := s.repo.Ping(ctx); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"degraded"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleIndex renders the overview table of every job.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.renderNotFound(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	jobs, err := s.repo.ListJobs(ctx)
	if err != nil {
		log.Printf("crons: list jobs: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q != "" {
		jobs = filterJobsByQuery(jobs, q)
	}

	status := r.URL.Query().Get("status")
	jobs = filterJobsByStatus(jobs, status)

	sortKey := r.URL.Query().Get("sort")
	dir := r.URL.Query().Get("dir")
	jobs = sortJobs(jobs, sortKey, dir)

	if err := s.indexTmpl.ExecuteTemplate(w, "layout", indexData{Jobs: jobs, Q: q, Status: status, Sort: sortKey, Dir: dir}); err != nil {
		log.Printf("crons: render index: %v", err)
	}
}

// filterJobsByQuery returns the subset of jobs whose name, jobID,
// display name, or description contains q as a case-insensitive
// substring. An empty q returns the input unchanged.
func filterJobsByQuery(jobs []domain.Job, q string) []domain.Job {
	needle := strings.ToLower(q)
	out := make([]domain.Job, 0, len(jobs))
	for _, j := range jobs {
		if strings.Contains(strings.ToLower(j.Name), needle) ||
			strings.Contains(strings.ToLower(j.JobID), needle) ||
			strings.Contains(strings.ToLower(j.DisplayName), needle) ||
			strings.Contains(strings.ToLower(j.Description), needle) {
			out = append(out, j)
		}
	}
	return out
}

// handleJobDetail renders the per-job detail page with recent run history.
func (s *Server) handleJobDetail(w http.ResponseWriter, r *http.Request) {
	id := jobIDFromPath(r.URL.Path)
	if id == "" {
		s.renderNotFound(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	job, err := s.repo.GetJob(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.renderNotFound(w)
			return
		}
		log.Printf("crons: get job %q: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	logs, err := s.repo.ListRunLogs(ctx, id, defaultRunLogLimit)
	if err != nil {
		log.Printf("crons: list run logs for %q: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.jobTmpl.ExecuteTemplate(w, "layout", jobData{Job: job, RunLogs: logs}); err != nil {
		log.Printf("crons: render job %q: %v", id, err)
	}
}

// renderNotFound renders the 404 page with HTTP 404 status.
func (s *Server) renderNotFound(w http.ResponseWriter) {
	// Set Content-Type before WriteHeader so the response carries the
	// right MIME type — once WriteHeader fires, the headers are flushed
	// and Content-Type auto-sniffing is skipped.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	if err := s.notFoundTmpl.ExecuteTemplate(w, "layout", nil); err != nil {
		log.Printf("crons: render 404: %v", err)
	}
}

// filterJobsByStatus returns the subset of jobs matching the given
// status. Recognised statuses: "ok", "error", "skipped", "disabled",
// "running". An empty or unknown status returns the input unchanged.
func filterJobsByStatus(jobs []domain.Job, status string) []domain.Job {
	if status == "" {
		return jobs
	}
	out := make([]domain.Job, 0, len(jobs))
	for _, j := range jobs {
		if jobMatchesStatus(j, status) {
			out = append(out, j)
		}
	}
	return out
}

func jobMatchesStatus(j domain.Job, status string) bool {
	switch status {
	case "ok":
		return j.Enabled && j.LastRunStatus == "ok"
	case "error":
		return j.LastRunStatus == "failed"
	case "skipped":
		return j.LastRunStatus == "skipped"
	case "disabled":
		return !j.Enabled
	case "running":
		return j.RunningAt != nil
	default:
		return true
	}
}

// sortJobs returns a copy of jobs sorted by the given axis and direction.
// Empty axis returns the input unchanged. An unknown axis returns the
// input unchanged (allowlist prevents SQL-injection-style abuse even
// though no SQL is involved).
func sortJobs(jobs []domain.Job, axis, dir string) []domain.Job {
	if axis == "" {
		return jobs
	}
	desc := dir == "desc"
	out := make([]domain.Job, len(jobs))
	copy(out, jobs)
	sort.SliceStable(out, func(i, j int) bool {
		return jobLess(out[i], out[j], axis, desc)
	})
	return out
}

func jobLess(a, b domain.Job, axis string, desc bool) bool {
	switch axis {
	case "name":
		if desc {
			return a.Name > b.Name
		}
		return a.Name < b.Name
	case "next":
		return timePtrLess(a.NextRunAt, b.NextRunAt, desc)
	case "last":
		return timePtrLess(a.LastRunAt, b.LastRunAt, desc)
	case "errors":
		if desc {
			return a.ConsecutiveErrors > b.ConsecutiveErrors
		}
		return a.ConsecutiveErrors < b.ConsecutiveErrors
	case "duration":
		if desc {
			return a.LastDurationMS > b.LastDurationMS
		}
		return a.LastDurationMS < b.LastDurationMS
	case "kind":
		if desc {
			return kindOrder(a.ScheduleKind) > kindOrder(b.ScheduleKind)
		}
		return kindOrder(a.ScheduleKind) < kindOrder(b.ScheduleKind)
	default:
		return false
	}
}

// timePtrLess orders two *time.Time values, with nil always sorting
// last regardless of direction.
func timePtrLess(a, b *time.Time, desc bool) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	if desc {
		return a.After(*b)
	}
	return a.Before(*b)
}

// kindOrder assigns a sort rank to schedule kinds. One-time reminders
// (at) sort first, intervals (every) next, cron patterns after that,
// then any unknown kind, and finally jobs with no kind.
func kindOrder(k string) int {
	switch k {
	case "at":
		return 0
	case "every":
		return 1
	case "cron":
		return 2
	case "":
		return 4
	default:
		return 3
	}
}
