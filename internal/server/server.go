// Package server renders the read-only cron-jobs dashboard. It depends
// only on the domain.Repository interface; the openclaw adapter is
// supplied at construction time.
package server

import (
	"crypto/subtle"
	"embed"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/simons-agent-space/cron-dashboard/internal/domain"
)

// requestTimeout is the per-request deadline for adapter calls.
const requestTimeout = 10 * time.Second

// defaultRunLogLimit is the number of recent runs shown on the job detail
// page. Hard cap: keep the dashboard page light even for jobs that have
// thousands of runs.
const defaultRunLogLimit = 20

//go:embed templates/*.html
var templateFS embed.FS

// funcMap exposes the formatting helpers to the templates.
var funcMap = template.FuncMap{
	"fmtSchedule":   fmtSchedule,
	"fmtTime":       fmtTime,
	"fmtTimeOpt":    fmtTimeOpt,
	"fmtDurationMS": fmtDurationMS,
}

// parsePage parses layout.html together with the given page so the layout's
// {{template "content" .}} resolves to that page's content block.
func parsePage(page string) (*template.Template, error) {
	return template.New("").Funcs(funcMap).ParseFS(templateFS,
		"templates/layout.html",
		page,
	)
}

// Server is the HTTP front-end. Configured once at startup, reused for
// every request.
type Server struct {
	repo         domain.Repository
	user         string
	pass         string
	authEnabled  bool
	indexTmpl    *template.Template
	jobTmpl      *template.Template
	notFoundTmpl *template.Template
}

// NewServer wires the dashboard. authEnabled controls whether dashboard
// routes (everything except /healthz) require Basic Auth. If one of
// user/pass is set but not the other, the constructor refuses to start.
func NewServer(repo domain.Repository, user, pass string, authEnabled bool) (*Server, error) {
	if authEnabled && (user == "" || pass == "") {
		return nil, errors.New("server: BASIC_AUTH_USER and BASIC_AUTH_PASSWORD must both be set when auth is enabled")
	}
	indexTmpl, err := parsePage("templates/index.html")
	if err != nil {
		return nil, err
	}
	jobTmpl, err := parsePage("templates/job.html")
	if err != nil {
		return nil, err
	}
	notFoundTmpl, err := parsePage("templates/notfound.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		repo:         repo,
		user:         user,
		pass:         pass,
		authEnabled:  authEnabled,
		indexTmpl:    indexTmpl,
		jobTmpl:      jobTmpl,
		notFoundTmpl: notFoundTmpl,
	}, nil
}

// Handler returns the root HTTP handler. /healthz is unauthenticated;
// every other route is protected when auth is enabled.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/jobs/", s.handleJobDetail)
	mux.HandleFunc("/", s.handleIndex)
	return s.authMiddleware(mux)
}

// authMiddleware enforces Basic Auth on every route except /healthz.
// When authEnabled is false, the middleware is a pass-through.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /healthz is always public so container and external probes
		// can reach it without credentials.
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		// Dashboard pages: prevent aggressive caching so deploys are
		// visible immediately on phones/browsers without manual refresh.
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		if !s.authEnabled {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(s.user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(s.pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="crons", charset="UTF-8"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// jobIDFromPath extracts the job id from /jobs/{id}. Returns "" if the
// path does not match the expected shape.
func jobIDFromPath(path string) string {
	const prefix = "/jobs/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	id := strings.TrimPrefix(path, prefix)
	id = strings.TrimSuffix(id, "/")
	return id
}
