// Command sentinel is the AI-Slop Triage Engine webhook listener.
// It wires config + collaborators and serves the verified /webhook endpoint.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/github"
	"sentinel/internal/triage"
	"sentinel/internal/verify"
	"sentinel/internal/webhook"
)

// statsWithVersion is the /stats response: the operational counters plus the
// gateway build version (FR-003, FR-004).
type statsWithVersion struct {
	webhook.Snapshot
	Version string `json:"version"`
}

// writeJSON writes v as a JSON body with a 200 status. Endpoint bodies are
// small and fixed-shape, so an encode error is only logged.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "err", err)
	}
}

// version is the gateway build version, set at link time via
//
//	-ldflags "-X main.version=$(git describe --tags --always)".
//
// It defaults to "dev" for a plain `go build` and is reported on /healthz and
// /stats so an operator can tell which build is running (FR-004).
var version = "dev"

func main() {
	// -healthz is the container healthcheck probe: the distroless image has no
	// shell or curl/wget, so the binary probes its own /healthz and exits 0/1.
	healthz := flag.Bool("healthz", false, "probe local /healthz and exit (container healthcheck)")
	flag.Parse()
	if *healthz {
		os.Exit(healthCheck())
	}

	// Structured JSON logs to stderr, machine-parseable and correlatable by the
	// delivery ID attached per-request downstream (FR-001). The default logger
	// is shared by every package via slog.Default().
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	gh := github.New(cfg.GitHubToken, cfg.GitHubAPIBase)
	tr := triage.New(cfg.ModelURL)

	// Source the confidence threshold from the artifact via the model's
	// /healthz unless the operator set CONFIDENCE_THRESHOLD explicitly (env
	// wins). Best-effort: if the model is down at startup, keep the built-in
	// default — the gateway must start independently of model health (FR-007).
	if !cfg.ThresholdFromEnv {
		hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
		if h, err := tr.FetchHealth(hctx); err != nil {
			slog.Warn("model healthz unavailable; using default threshold", "err", err, "threshold", cfg.ConfidenceThreshold)
		} else if h.Threshold > 0 {
			cfg.ConfidenceThreshold = h.Threshold
			slog.Info("threshold sourced from model artifact", "threshold", h.Threshold, "artifact", h.Artifact)
		}
		hcancel()
	}

	handler := webhook.New(*cfg, gh, tr)
	handler.Start() // launch the triage worker pool

	mux := http.NewServeMux()
	mux.Handle("/webhook", verify.Middleware(cfg.WebhookSecret, cfg.MaxBodyBytes, handler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"status": "ok", "version": version})
	})
	// /stats reports operational counters + triage latency (FR-003). It needs no
	// secret and exposes no PR content — counts and timings only.
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, statsWithVersion{Snapshot: handler.Stats(), Version: version})
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Shut down on SIGINT/SIGTERM: stop accepting connections, then drain
	// in-flight triage so an accepted delivery is not lost mid-process.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		stop() // restore default signal handling; a second signal now aborts
		slog.Info("shutdown: draining")

		shutCtx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			slog.Error("http shutdown", "err", err)
		}
		if err := handler.Shutdown(shutCtx); err != nil {
			slog.Error("worker drain", "err", err)
		}
	}()

	slog.Info("sentinel listening",
		"port", cfg.Port,
		"threshold", cfg.ConfidenceThreshold,
		"label", cfg.SlopLabel,
		"shadow_mode", cfg.ShadowMode,
		"version", version,
	)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}

// healthCheck probes the local /healthz endpoint and returns a process exit code
// (0 healthy, 1 otherwise). PORT is read directly — no config.Load — so the probe
// stays lightweight and doesn't depend on the full secret set being present.
func healthCheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		slog.Error("healthz probe", "err", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Error("healthz probe", "status", resp.StatusCode)
		return 1
	}
	return 0
}
