// Command sentinel is the AI-Slop Triage Engine webhook listener.
// It wires config + collaborators and serves the verified /webhook endpoint.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
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

func main() {
	// -healthz is the container healthcheck probe: the distroless image has no
	// shell or curl/wget, so the binary probes its own /healthz and exits 0/1.
	healthz := flag.Bool("healthz", false, "probe local /healthz and exit (container healthcheck)")
	flag.Parse()
	if *healthz {
		os.Exit(healthCheck())
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
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
			log.Printf("threshold: model healthz unavailable (%v); using default %.2f", err, cfg.ConfidenceThreshold)
		} else if h.Threshold > 0 {
			cfg.ConfidenceThreshold = h.Threshold
			log.Printf("threshold: sourced %.2f from model artifact %q", h.Threshold, h.Artifact)
		}
		hcancel()
	}

	handler := webhook.New(*cfg, gh, tr)
	handler.Start() // launch the triage worker pool

	mux := http.NewServeMux()
	mux.Handle("/webhook", verify.Middleware(cfg.WebhookSecret, cfg.MaxBodyBytes, handler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
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
		log.Print("shutdown: draining...")

		shutCtx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Printf("http shutdown: %v", err)
		}
		if err := handler.Shutdown(shutCtx); err != nil {
			log.Printf("worker drain: %v", err)
		}
	}()

	log.Printf("sentinel listening on :%s (threshold=%.2f, label=%q)", cfg.Port, cfg.ConfidenceThreshold, cfg.SlopLabel)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
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
		log.Printf("healthz probe: %v", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("healthz probe: status %d", resp.StatusCode)
		return 1
	}
	return 0
}
