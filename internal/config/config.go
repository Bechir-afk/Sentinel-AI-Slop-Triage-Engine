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
	ShadowMode          bool    // SHADOW_MODE — run pipeline, log verdict, write nothing
	ModelThreads        int     // MODEL_THREADS — model service CPU thread cap (passed through)
	GitHubMaxRetries    int     // GITHUB_MAX_RETRIES — bounded transient-error retries
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
		ModelThreads:        4,
		GitHubMaxRetries:    2,
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

	if raw := os.Getenv("SHADOW_MODE"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("SHADOW_MODE %q is not a boolean: %w", raw, err)
		}
		c.ShadowMode = v
	}

	if err := envInt(&c.ModelThreads, "MODEL_THREADS", 1, 64); err != nil {
		return nil, err
	}
	if err := envInt(&c.GitHubMaxRetries, "GITHUB_MAX_RETRIES", 0, 5); err != nil {
		return nil, err
	}

	return c, nil
}

// envInt parses an optional integer env var into *dst, validating it falls
// within [min,max]. An unset var leaves *dst (the caller's default) untouched.
func envInt(dst *int, key string, min, max int) error {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("%s %q is not an integer: %w", key, raw, err)
	}
	if v < min || v > max {
		return fmt.Errorf("%s %d out of range [%d,%d]", key, v, min, max)
	}
	*dst = v
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
