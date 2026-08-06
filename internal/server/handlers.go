package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/simons-agent-space/cron-dashboard/internal/domain"
)

// indexData is the data the index template renders.
type indexData struct {
	Jobs   []domain.Job
	Q      string
	Status string
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

	if err := s.indexTmpl.ExecuteTemplate(w, "layout", indexData{Jobs: jobs, Q: q, Status: status}); err != nil {
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
