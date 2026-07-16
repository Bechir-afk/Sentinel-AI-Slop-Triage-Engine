// Command sentinel is the AI-Slop Triage Engine webhook listener.
// It wires config + collaborators and serves the verified /webhook endpoint.
package main

import (
	"log"
	"net/http"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/github"
	"sentinel/internal/triage"
	"sentinel/internal/verify"
	"sentinel/internal/webhook"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	gh := github.New(cfg.GitHubToken, cfg.GitHubAPIBase)
	tr := triage.New(cfg.ModelURL)
	handler := webhook.New(*cfg, gh, tr)

	mux := http.NewServeMux()
	mux.Handle("/webhook", verify.Middleware(cfg.WebhookSecret, handler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("sentinel listening on :%s (threshold=%.2f, label=%q)", cfg.Port, cfg.ConfidenceThreshold, cfg.SlopLabel)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
