// Package config loads Sentinel's runtime configuration from environment
// variables (12-factor). No config files ship in the image.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds every value the service needs to run.
type Config struct {
	WebhookSecret       string  // GITHUB_WEBHOOK_SECRET — HMAC shared secret
	GitHubToken         string  // GITHUB_TOKEN — fetch diff + write label/comment
	ModelURL            string  // MODEL_URL — base URL of the model service
	ConfidenceThreshold float64 // CONFIDENCE_THRESHOLD — min confidence to act
	ThresholdFromEnv    bool    // true if CONFIDENCE_THRESHOLD was set explicitly
	SlopLabel           string  // SLOP_LABEL — label applied to flagged PRs
	Port                string  // PORT — listen port
	GitHubAPIBase       string  // GitHub REST base (overridable for tests)
	MaxBodyBytes        int64   // MAX_BODY_BYTES — cap on webhook body size (bytes)
	WorkerCount         int     // WORKER_COUNT — triage worker-pool size
}

// Load reads and validates configuration. Required secrets that are missing
// cause a hard error so the service never starts half-configured.
func Load() (*Config, error) {
	c := &Config{
		WebhookSecret:       os.Getenv("GITHUB_WEBHOOK_SECRET"),
		GitHubToken:         os.Getenv("GITHUB_TOKEN"),
		ModelURL:            os.Getenv("MODEL_URL"),
		ConfidenceThreshold: 0.95, // conservative fallback; model /healthz or env override replaces it
		SlopLabel:           envOr("SLOP_LABEL", "needs-human-review"),
		Port:                envOr("PORT", "8080"),
		GitHubAPIBase:       envOr("GITHUB_API_BASE", "https://api.github.com"),
		MaxBodyBytes:        25 << 20, // 26214400 (25 MiB); GitHub caps deliveries ~25MB
		WorkerCount:         8,
	}

	for name, val := range map[string]string{
		"GITHUB_WEBHOOK_SECRET": c.WebhookSecret,
		"GITHUB_TOKEN":          c.GitHubToken,
		"MODEL_URL":             c.ModelURL,
	} {
		if val == "" {
			return nil, fmt.Errorf("required env var %s is not set", name)
		}
	}

	if raw := os.Getenv("CONFIDENCE_THRESHOLD"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("CONFIDENCE_THRESHOLD %q is not a number: %w", raw, err)
		}
		if v < 0 || v > 1 {
			return nil, fmt.Errorf("CONFIDENCE_THRESHOLD %v out of range [0,1]", v)
		}
		c.ConfidenceThreshold = v
		c.ThresholdFromEnv = true
	}

	if raw := os.Getenv("MAX_BODY_BYTES"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("MAX_BODY_BYTES %q is not an integer: %w", raw, err)
		}
		if v < 1024 {
			return nil, fmt.Errorf("MAX_BODY_BYTES %d below the 1 KiB minimum", v)
		}
		c.MaxBodyBytes = v
	}

	if raw := os.Getenv("WORKER_COUNT"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("WORKER_COUNT %q is not an integer: %w", raw, err)
		}
		if v < 1 || v > 64 {
			return nil, fmt.Errorf("WORKER_COUNT %d out of range [1,64]", v)
		}
		c.WorkerCount = v
	}

	return c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
