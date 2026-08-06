package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/simons-agent-space/cron-dashboard/internal/domain"
)

// fakeRepo is a hand-rolled test double for the read paths the dashboard
// exercises. Avoids spinning up a real SQLite, which the openclaw tests
// cover.
type fakeRepo struct {
	jobs    []domain.Job
	logs    map[string][]domain.RunLog
	pingErr error
	getErr  map[string]error
}

func (f *fakeRepo) ListJobs(_ context.Context) ([]domain.Job, error) {
	return f.jobs, nil
}
func (f *fakeRepo) GetJob(_ context.Context, id string) (*domain.Job, error) {
	if err, ok := f.getErr[id]; ok {
		return nil, err
	}
	for i := range f.jobs {
		if f.jobs[i].JobID == id {
			return &f.jobs[i], nil
		}
	}
	return nil, domain.ErrNotFound
}
func (f *fakeRepo) ListRunLogs(_ context.Context, jobID string, limit int) ([]domain.RunLog, error) {
	logs := f.logs[jobID]
	if limit > 0 && len(logs) > limit {
		logs = logs[:limit]
	}
	return logs, nil
}
func (f *fakeRepo) Ping(_ context.Context) error { return f.pingErr }

func newTestServer(t *testing.T, repo domain.Repository, user, pass string, authEnabled bool) *Server {
	t.Helper()
	s, err := NewServer(repo, user, pass, authEnabled)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return s
}

func ts(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestNewServer_RejectsHalfConfiguredAuth(t *testing.T) {
	for _, c := range []struct {
		name        string
		user, pass  string
		authEnabled bool
	}{
		{"user-only", "alice", "", true},
		{"pass-only", "", "hunter2", true},
		{"empty-empty", "", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewServer(&fakeRepo{}, c.user, c.pass, c.authEnabled)
			if err == nil {
				t.Fatalf("expected NewServer to refuse half-configured auth")
			}
		})
	}
}

func TestHealthz_OK(t *testing.T) {
	s := newTestServer(t, &fakeRepo{}, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("expected ok status, got %s", body)
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("expected json content type, got %q", got)
	}
}

func TestHealthz_DegradedWhenPingFails(t *testing.T) {
	repo := &fakeRepo{pingErr: errors.New("disk on fire")}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"status":"degraded"`) {
		t.Fatalf("expected degraded status, got %s", rr.Body.String())
	}
}

func TestHealthz_AlwaysPublic_EvenWhenAuthEnabled(t *testing.T) {
	// /healthz must be reachable without credentials so container probes
	// and external probes can hit it.
	s := newTestServer(t, &fakeRepo{}, "alice", "hunter2", true)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 without auth on /healthz, got %d", rr.Code)
	}
}

func TestIndex_RendersOverview(t *testing.T) {
	nextRun := ts(2026, 8, 5, 3, 0)
	lastRun := ts(2026, 8, 4, 3, 0)
	repo := &fakeRepo{
		jobs: []domain.Job{
			{
				JobID:              "job-1",
				Name:               "nightly-cleanup",
				Description:        "delete stale tmp files",
				Enabled:            true,
				ScheduleKind:       "cron",
				ScheduleExpr:       "0 3 * * *",
				ScheduleTZ:         "Europe/Berlin",
				NextRunAt:          ptrTime(nextRun),
				LastRunAt:          ptrTime(lastRun),
				LastRunStatus:      "ok",
				LastDurationMS:     1200,
				ConsecutiveErrors:  0,
				LastDeliveryStatus: "delivered",
			},
			{
				JobID:        "job-2",
				Name:         "broken",
				Enabled:      false,
				ScheduleKind: "every",
				EveryMS:      900_000,
			},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("expected text/html, got %q", got)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"nightly-cleanup",
		"broken",
		"cron 0 3 * * * (Europe/Berlin)",
		"every 15m",
		"delivered",
		`<a href="/jobs/job-1">`,
		"disabled",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q\n---\n%s", want, body)
		}
	}
}

func TestIndex_EmptyState(t *testing.T) {
	s := newTestServer(t, &fakeRepo{}, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "No jobs found.") {
		t.Fatalf("expected empty-state message, got %s", rr.Body.String())
	}
}

func TestIndex_HTMLEscapesJobData(t *testing.T) {
	// XSS guard: a job name that contains HTML must be escaped when
	// rendered, never injected raw.
	repo := &fakeRepo{
		jobs: []domain.Job{
			{
				JobID:        "<img src=x onerror=alert(1)>",
				Name:         "<script>alert('xss')</script>",
				Description:  "<b>not bold</b>",
				Enabled:      true,
				ScheduleKind: "cron",
				ScheduleExpr: "* * * * *",
			},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "<script>alert('xss')</script>") {
		t.Fatalf("XSS: raw <script> tag in body:\n%s", body)
	}
	if strings.Contains(body, "<img src=x onerror=alert(1)>") {
		t.Fatalf("XSS: raw <img onerror> in body:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("expected escaped &lt;script&gt; in body, got:\n%s", body)
	}
	if !strings.Contains(body, "&lt;b&gt;not bold&lt;/b&gt;") {
		t.Fatalf("expected escaped description, got:\n%s", body)
	}
}

func TestJobDetail_RendersDetailAndHistory(t *testing.T) {
	job := domain.Job{
		JobID:              "job-1",
		Name:               "nightly-cleanup",
		Description:        "delete stale tmp files",
		Enabled:            true,
		ScheduleKind:       "cron",
		ScheduleExpr:       "0 3 * * *",
		ScheduleTZ:         "Europe/Berlin",
		NextRunAt:          ptrTime(ts(2026, 8, 5, 3, 0)),
		LastRunAt:          ptrTime(ts(2026, 8, 4, 3, 0)),
		LastRunStatus:      "ok",
		LastDurationMS:     1200,
		LastDeliveryStatus: "delivered",
	}
	repo := &fakeRepo{
		jobs: []domain.Job{job},
		logs: map[string][]domain.RunLog{
			"job-1": {
				{JobID: "job-1", Seq: 2, Ts: ts(2026, 8, 4, 3, 0), Status: "ok", DurationMS: 1200},
				{JobID: "job-1", Seq: 1, Ts: ts(2026, 8, 3, 3, 0), Status: "failed", Error: "boom"},
			},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/jobs/job-1", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("expected text/html, got %q", got)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"nightly-cleanup",
		"delete stale tmp files",
		"Recent runs",
		"boom",
		"delivered",
		`<a href="/">&larr; back to overview</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q\n---\n%s", want, body)
		}
	}
}

func TestJobDetail_NotFoundRendersHTML404(t *testing.T) {
	s := newTestServer(t, &fakeRepo{}, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/jobs/missing", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("expected HTML 404, got content-type %q", got)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Not found") {
		t.Fatalf("expected 404 page body, got:\n%s", body)
	}
}

func TestJobDetail_HTMLEscapesJobData(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{
			{
				JobID:        "evil",
				Name:         "<script>alert('xss')</script>",
				Enabled:      true,
				ScheduleKind: "cron",
				ScheduleExpr: "* * * * *",
				LastError:    "<bad>tags</bad>",
			},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/jobs/evil", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "<script>alert('xss')</script>") {
		t.Fatalf("XSS in detail page:\n%s", body)
	}
	if strings.Contains(body, "<bad>tags</bad>") {
		t.Fatalf("XSS in last_error rendering:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("expected escaped name:\n%s", body)
	}
}

func TestJobDetail_RendersEmptyRunHistoryMessage(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{
			{JobID: "job-1", Name: "x", Enabled: true, ScheduleKind: "cron", ScheduleExpr: "* * * * *"},
		},
		logs: map[string][]domain.RunLog{"job-1": nil},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/jobs/job-1", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "No runs recorded yet.") {
		t.Fatalf("expected empty-runs message, got:\n%s", rr.Body.String())
	}
}

func TestAuth_RequiredWhenEnabled(t *testing.T) {
	s := newTestServer(t, &fakeRepo{}, "alice", "hunter2", true)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", rr.Code)
	}
	if got := rr.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Basic") {
		t.Fatalf("expected WWW-Authenticate header, got %q", got)
	}
}

func TestAuth_AcceptsValidCredentials(t *testing.T) {
	s := newTestServer(t, &fakeRepo{}, "alice", "hunter2", true)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("alice", "hunter2")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid auth, got %d", rr.Code)
	}
}

func TestAuth_RejectsWrongCredentials(t *testing.T) {
	s := newTestServer(t, &fakeRepo{}, "alice", "hunter2", true)

	for _, c := range []struct {
		name       string
		user, pass string
	}{
		{"wrong-password", "alice", "wrong"},
		{"wrong-user", "bob", "hunter2"},
		{"both-wrong", "bob", "wrong"},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.SetBasicAuth(c.user, c.pass)
			rr := httptest.NewRecorder()
			s.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rr.Code)
			}
		})
	}
}

func TestAuth_DisabledAllowsEverything(t *testing.T) {
	s := newTestServer(t, &fakeRepo{}, "", "", false)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with auth disabled, got %d", rr.Code)
	}
}

func TestAuth_RequiredForJobDetail(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{{JobID: "job-1", Name: "x", Enabled: true, ScheduleKind: "cron", ScheduleExpr: "* * * * *"}},
	}
	s := newTestServer(t, repo, "alice", "hunter2", true)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/jobs/job-1", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestFmtTimeOpt_HandlesNil(t *testing.T) {
	if got := fmtTimeOpt(nil); got != "—" {
		t.Fatalf("expected em dash for nil, got %q", got)
	}
	if got := fmtTimeOpt(&time.Time{}); got != "—" {
		t.Fatalf("expected em dash for zero, got %q", got)
	}
}

func TestFmtDurationMS(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "—"},
		{-1, "—"},
		{500, "500ms"},
		{1500, "1s"},
		{90_000, "1m 30s"},
		{3_600_000, "1h"},
		{86_400_000, "1d"},
	}
	for _, c := range cases {
		if got := fmtDurationMS(c.ms); got != c.want {
			t.Errorf("fmtDurationMS(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}

func TestFmtSchedule(t *testing.T) {
	cases := []struct {
		name string
		job  domain.Job
		want string
	}{
		{"cron-with-tz", domain.Job{ScheduleKind: "cron", ScheduleExpr: "0 3 * * *", ScheduleTZ: "Europe/Berlin"}, "cron 0 3 * * * (Europe/Berlin)"},
		{"cron-no-tz", domain.Job{ScheduleKind: "cron", ScheduleExpr: "*/5 * * * *"}, "cron */5 * * * *"},
		{"every", domain.Job{ScheduleKind: "every", EveryMS: 900_000}, "every 15m"},
		{"at", domain.Job{ScheduleKind: "at", At: "2026-09-01T00:00:00Z"}, "at 2026-09-01T00:00:00Z"},
		{"unknown", domain.Job{ScheduleKind: "weird"}, "weird"},
		{"empty", domain.Job{}, "—"},
		{"cron-no-expr", domain.Job{ScheduleKind: "cron"}, "cron (no expression)"},
		{"every-zero", domain.Job{ScheduleKind: "every"}, "every ?"},
		{"at-empty", domain.Job{ScheduleKind: "at"}, "at (no value)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fmtSchedule(c.job); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestJobIDFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/jobs/job-1", "job-1"},
		{"/jobs/some/weird/path", "some/weird/path"},
		{"/jobs/", ""},
		{"/random", ""},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := jobIDFromPath(c.path); got != c.want {
				t.Errorf("jobIDFromPath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

func TestIndex_FiltersByQuery(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{
			{JobID: "job-1", Name: "nightly-cleanup", Description: "delete tmp files"},
			{JobID: "job-2", Name: "weekly-report"},
			{JobID: "job-3", Name: "disk-monitor", Description: "watch /var"},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?q=nightly", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "nightly-cleanup") {
		t.Errorf("expected nightly-cleanup in results:\n%s", body)
	}
	if strings.Contains(body, "weekly-report") {
		t.Errorf("expected weekly-report filtered out:\n%s", body)
	}
	if strings.Contains(body, "disk-monitor") {
		t.Errorf("expected disk-monitor filtered out:\n%s", body)
	}
	if !strings.Contains(body, `value="nightly"`) {
		t.Errorf("expected search input to retain value:\n%s", body)
	}
}

func TestIndex_EmptyQueryReturnsAll(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{
			{JobID: "job-1", Name: "alpha"},
			{JobID: "job-2", Name: "beta"},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?q=", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "alpha") || !strings.Contains(body, "beta") {
		t.Fatalf("expected both jobs with empty query, got:\n%s", body)
	}
	if !strings.Contains(body, `value=""`) {
		t.Fatalf("expected empty search input value, got:\n%s", body)
	}
}

func TestIndex_QueryNoMatchShowsEmptyState(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{
			{JobID: "job-1", Name: "alpha"},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?q=nothingmatches", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "alpha") {
		t.Errorf("expected alpha filtered out:\n%s", body)
	}
	if !strings.Contains(body, "No jobs found.") {
		t.Errorf("expected empty-state message, got:\n%s", body)
	}
}

func TestFilterJobsByQuery(t *testing.T) {
	jobs := []domain.Job{
		{JobID: "id-1", Name: "alpha", DisplayName: "Alpha", Description: "first"},
		{JobID: "id-2", Name: "beta"},
		{JobID: "id-3", Name: "gamma", Description: "third alpha-ish"},
	}

	cases := []struct {
		name string
		q    string
		want []string
	}{
		{"empty-returns-all", "", []string{"id-1", "id-2", "id-3"}},
		{"matches-name", "alpha", []string{"id-1", "id-3"}}, // id-3 description "third alpha-ish" also matches
		{"matches-id", "id-2", []string{"id-2"}},
		{"matches-display", "Alpha", []string{"id-1", "id-3"}}, // case-insensitive
		{"matches-description", "third", []string{"id-3"}},
		{"no-match", "nope", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := filterJobsByQuery(jobs, c.q)
			if len(got) != len(c.want) {
				t.Fatalf("got %d jobs, want %d (%v)", len(got), len(c.want), got)
			}
			for i, j := range got {
				if j.JobID != c.want[i] {
					t.Errorf("position %d: got %s, want %s", i, j.JobID, c.want[i])
				}
			}
		})
	}
}

func TestIndex_QueryTrimsWhitespace(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{
			{JobID: "job-1", Name: "alpha"},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?q=%20%20alpha%20%20", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "alpha") {
		t.Fatalf("expected alpha after trim, got:\n%s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `value="alpha"`) {
		t.Fatalf("expected trimmed search input value, got:\n%s", rr.Body.String())
	}
}

func TestIndex_FiltersByStatus(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{
			{JobID: "job-1", Name: "ok-job", Enabled: true, LastRunStatus: "ok"},
			{JobID: "job-2", Name: "broken-job", Enabled: true, LastRunStatus: "failed"},
			{JobID: "job-3", Name: "off-job", Enabled: false, LastRunStatus: "ok"},
			{JobID: "job-4", Name: "skipped-job", Enabled: true, LastRunStatus: "skipped"},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?status=error", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "broken-job") {
		t.Errorf("expected broken-job in results:\n%s", body)
	}
	if strings.Contains(body, "ok-job") {
		t.Errorf("expected ok-job filtered out:\n%s", body)
	}
	if strings.Contains(body, "off-job") {
		t.Errorf("expected off-job filtered out:\n%s", body)
	}
	if strings.Contains(body, "skipped-job") {
		t.Errorf("expected skipped-job filtered out:\n%s", body)
	}
	if !strings.Contains(body, `chip-active`) {
		t.Errorf("expected active chip styling, got:\n%s", body)
	}
}

func TestIndex_StatusAndQueryCombined(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{
			{JobID: "job-1", Name: "nightly", Enabled: true, LastRunStatus: "ok"},
			{JobID: "job-2", Name: "weekly", Enabled: true, LastRunStatus: "failed"},
			{JobID: "job-3", Name: "nightly-old", Enabled: false, LastRunStatus: "ok"},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?q=nightly&status=ok", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `nightly</a>`) {
		t.Errorf("expected nightly in results:\n%s", body)
	}
	if strings.Contains(body, "weekly") {
		t.Errorf("expected weekly filtered out by query:\n%s", body)
	}
	if strings.Contains(body, "nightly-old") {
		t.Errorf("expected nightly-old filtered out by status (disabled):\n%s", body)
	}
}

func TestIndex_StatusUnknownNoOp(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{
			{JobID: "job-1", Name: "alpha", Enabled: true, LastRunStatus: "ok"},
			{JobID: "job-2", Name: "beta", Enabled: true, LastRunStatus: "failed"},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?status=banana", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "alpha") || !strings.Contains(body, "beta") {
		t.Fatalf("unknown status should not filter, got:\n%s", body)
	}
}

func TestFilterJobsByStatus(t *testing.T) {
	running := ts(2026, 8, 6, 12, 0)
	jobs := []domain.Job{
		{JobID: "ok", Name: "ok-job", Enabled: true, LastRunStatus: "ok"},
		{JobID: "failed", Name: "failed-job", Enabled: true, LastRunStatus: "failed"},
		{JobID: "skipped", Name: "skipped-job", Enabled: true, LastRunStatus: "skipped"},
		{JobID: "off", Name: "off-job", Enabled: false, LastRunStatus: "ok"},
		{JobID: "running", Name: "running-job", Enabled: true, LastRunStatus: "ok", RunningAt: ptrTime(running)},
	}

	cases := []struct {
		name   string
		status string
		want   []string
	}{
		{"empty-no-filter", "", []string{"ok", "failed", "skipped", "off", "running"}},
		{"ok", "ok", []string{"ok", "running"}}, // disabled excluded; running counts as ok
		{"error", "error", []string{"failed"}},
		{"skipped", "skipped", []string{"skipped"}},
		{"disabled", "disabled", []string{"off"}},
		{"running", "running", []string{"running"}},
		{"unknown-no-op", "banana", []string{"ok", "failed", "skipped", "off", "running"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := filterJobsByStatus(jobs, c.status)
			if len(got) != len(c.want) {
				t.Fatalf("got %d jobs, want %d (%v)", len(got), len(c.want), got)
			}
			for i, j := range got {
				if j.JobID != c.want[i] {
					t.Errorf("position %d: got %s, want %s", i, j.JobID, c.want[i])
				}
			}
		})
	}
}

func TestSortJobs(t *testing.T) {
	t1 := ts(2026, 8, 5, 3, 0)
	t2 := ts(2026, 8, 6, 3, 0)
	t3 := ts(2026, 8, 4, 3, 0)
	jobs := []domain.Job{
		{JobID: "z", Name: "zebra", NextRunAt: ptrTime(t2), LastRunAt: ptrTime(t3), ConsecutiveErrors: 2, LastDurationMS: 500},
		{JobID: "a", Name: "alpha", NextRunAt: ptrTime(t1), LastRunAt: ptrTime(t1), ConsecutiveErrors: 0, LastDurationMS: 100},
		{JobID: "m", Name: "mango", NextRunAt: nil, LastRunAt: nil, ConsecutiveErrors: 5, LastDurationMS: 300},
		{JobID: "b", Name: "beta", NextRunAt: ptrTime(t2), LastRunAt: ptrTime(t2), ConsecutiveErrors: 0, LastDurationMS: 200},
	}

	cases := []struct {
		name string
		axis string
		dir  string
		want []string
	}{
		{"empty-no-sort", "", "", []string{"z", "a", "m", "b"}},
		{"name-asc", "name", "asc", []string{"a", "b", "m", "z"}},
		{"name-desc", "name", "desc", []string{"z", "m", "b", "a"}},
		{"next-asc-nil-last", "next", "asc", []string{"a", "z", "b", "m"}},
		{"next-desc-nil-last", "next", "desc", []string{"z", "b", "a", "m"}},
		{"last-asc-nil-last", "last", "asc", []string{"z", "a", "b", "m"}},
		{"errors-desc", "errors", "desc", []string{"m", "z", "a", "b"}},
		{"duration-asc", "duration", "asc", []string{"a", "b", "m", "z"}},
		{"unknown-no-op", "banana", "", []string{"z", "a", "m", "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sortJobs(jobs, c.axis, c.dir)
			if len(got) != len(c.want) {
				t.Fatalf("got %d jobs, want %d (%v)", len(got), len(c.want), got)
			}
			for i, j := range got {
				if j.JobID != c.want[i] {
					t.Errorf("position %d: got %s, want %s", i, j.JobID, c.want[i])
				}
			}
		})
	}
}

func TestIndex_SortByName(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{
			{JobID: "z", Name: "zebra"},
			{JobID: "a", Name: "alpha"},
			{JobID: "m", Name: "mango"},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?sort=name&dir=asc", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	// Verify order: alpha appears before mango, mango before zebra.
	alphaIdx := strings.Index(body, "alpha")
	mangoIdx := strings.Index(body, "mango")
	zebraIdx := strings.Index(body, "zebra")
	if !(alphaIdx < mangoIdx && mangoIdx < zebraIdx) {
		t.Fatalf("expected alpha < mango < zebra, got alpha=%d mango=%d zebra=%d\n%s",
			alphaIdx, mangoIdx, zebraIdx, body)
	}
}

func TestIndex_SortHeadersAreLinks(t *testing.T) {
	repo := &fakeRepo{
		jobs: []domain.Job{
			{
				JobID:         "job-1",
				Name:          "nightly-cleanup",
				Enabled:       true,
				LastRunStatus: "failed",
				NextRunAt:     ptrTime(ts(2026, 8, 5, 3, 0)),
			},
		},
	}
	s := newTestServer(t, repo, "", "", false)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?q=nightly&status=error", nil)
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	// Each sortable axis should appear as a sort param, dir should be
	// the toggle target, and q + status must be preserved. url.Values
	// sorts keys alphabetically so we do not assert on full URL order.
	for _, want := range []string{
		`sort=name`,
		`sort=next`,
		`sort=last`,
		`sort=errors`,
		`sort=duration`,
		`dir=asc`,
		`q=nightly`,
		`status=error`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body", want)
		}
	}
}
