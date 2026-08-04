// Command crons is the read-only web dashboard for OpenClaw cron jobs.
// It opens a SQLite database in read-only mode and serves a small HTML UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/simons-agent-space/crons/internal/openclaw"
	"github.com/simons-agent-space/crons/internal/server"
)

// DefaultDBPath is the absolute path the dashboard falls back to when
// OPENCLAW_DB_PATH is not set. In the deployment, /data/openclaw-state
// is the read-only mount of the OpenClaw state directory.
const DefaultDBPath = "/data/openclaw-state/openclaw.sqlite"

// DefaultListenAddr is the bind address when the operator does not
// override it.
const DefaultListenAddr = ":8080"

func main() {
	if err := run(); err != nil {
		log.Fatalf("crons: %v", err)
	}
}

func run() error {
	dbPath := strings.TrimSpace(os.Getenv("OPENCLAW_DB_PATH"))
	if dbPath == "" {
		dbPath = DefaultDBPath
	}

	// SQLite URI: mode=ro opens the database strictly read-only. The
	// modernc.org/sqlite driver requires the file:// scheme on absolute
	// paths.
	dsn := fmt.Sprintf("file:%s?mode=ro", dbPath)

	user, userSet := os.LookupEnv("BASIC_AUTH_USER")
	pass, passSet := os.LookupEnv("BASIC_AUTH_PASSWORD")
	if userSet != passSet {
		return errors.New("BASIC_AUTH_USER and BASIC_AUTH_PASSWORD must be set together")
	}
	authEnabled := userSet && passSet
	if !authEnabled {
		log.Print("WARNING: BASIC_AUTH_USER and BASIC_AUTH_PASSWORD are not set; authentication is disabled")
	} else {
		log.Print("HTTP Basic Auth enabled for dashboard routes; /healthz is public")
	}

	// The adapter is lazy: New only stores the DSN, the database file is
	// opened on first use. The binary therefore starts even when the
	// data directory is not yet mounted, and /healthz can report the
	// degraded state honestly.
	adapter := openclaw.New(dsn)
	defer func() {
		if err := adapter.Close(); err != nil {
			log.Printf("close database: %v", err)
		}
	}()

	// Probe the database once at startup for a fast log line. Failure is
	// not fatal: /healthz will keep reporting degraded until the database
	// becomes available.
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := adapter.Ping(probeCtx); err != nil {
		log.Printf("WARNING: database not reachable at startup (%v); /healthz will report degraded until it is", err)
	} else {
		log.Printf("database reachable at %s", dbPath)
	}
	probeCancel()

	srv, err := server.NewServer(adapter, user, pass, authEnabled)
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}

	addr := strings.TrimSpace(os.Getenv("HTTP_ADDR"))
	if addr == "" {
		addr = DefaultListenAddr
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s, database=%s", addr, dbPath)
		errCh <- httpServer.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-stop:
		log.Printf("received %s, shutting down", sig)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
