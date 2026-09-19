// Command sentinel is the AI-Slop Triage Engine webhook listener.
// It wires config + collaborators and serves the verified /webhook endpoint.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
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
