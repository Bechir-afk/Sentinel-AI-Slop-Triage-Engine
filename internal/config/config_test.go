package config

import "testing"

// setRequired sets the three mandatory vars and clears every optional so a test
// sees defaults, not values leaking from the real environment. t.Setenv restores
// all of them when the test ends.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_WEBHOOK_SECRET", "secret")
	t.Setenv("GITHUB_TOKEN", "token")
	t.Setenv("MODEL_URL", "http://model:9000")
	for _, k := range []string{"CONFIDENCE_THRESHOLD", "SLOP_LABEL", "PORT", "GITHUB_API_BASE", "MAX_BODY_BYTES", "WORKER_COUNT", "SHADOW_MODE", "MODEL_THREADS", "GITHUB_MAX_RETRIES"} {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ConfidenceThreshold != 0.95 {
		t.Errorf("ConfidenceThreshold = %v, want 0.95", c.ConfidenceThreshold)
	}
	if c.ThresholdFromEnv {
		t.Error("ThresholdFromEnv should be false when CONFIDENCE_THRESHOLD is unset")
	}
	if c.SlopLabel != "needs-human-review" {
		t.Errorf("SlopLabel = %q, want needs-human-review", c.SlopLabel)
	}
	if c.Port != "8080" {
		t.Errorf("Port = %q, want 8080", c.Port)
	}
	if c.MaxBodyBytes != 25<<20 {
		t.Errorf("MaxBodyBytes = %d, want %d", c.MaxBodyBytes, 25<<20)
	}
	if c.WorkerCount != 8 {
		t.Errorf("WorkerCount = %d, want 8", c.WorkerCount)
	}
	if c.ShadowMode {
		t.Error("ShadowMode should default to false")
	}
	if c.ModelThreads != 4 {
		t.Errorf("ModelThreads = %d, want 4", c.ModelThreads)
	}
	if c.GitHubMaxRetries != 2 {
		t.Errorf("GitHubMaxRetries = %d, want 2", c.GitHubMaxRetries)
	}
	if c.GitHubAPIBase != "https://api.github.com" {
		t.Errorf("GitHubAPIBase = %q", c.GitHubAPIBase)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	for _, missing := range []string{"GITHUB_WEBHOOK_SECRET", "GITHUB_TOKEN", "MODEL_URL"} {
		t.Run(missing, func(t *testing.T) {
			setRequired(t)
			t.Setenv(missing, "")
			if _, err := Load(); err == nil {
				t.Errorf("expected error when %s is unset", missing)
			}
		})
	}
}

func TestLoadConfidenceThreshold(t *testing.T) {
	tests := []struct {
		name    string
		val     string
		wantErr bool
		want    float64
	}{
		{"valid", "0.75", false, 0.75},
		{"not a number", "high", true, 0},
		{"below range", "-0.1", true, 0},
		{"above range", "1.5", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("CONFIDENCE_THRESHOLD", tc.val)
			c, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.val)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.ConfidenceThreshold != tc.want {
				t.Errorf("ConfidenceThreshold = %v, want %v", c.ConfidenceThreshold, tc.want)
			}
		})
	}
}

func TestLoadMaxBodyBytes(t *testing.T) {
	tests := []struct {
		name    string
		val     string
		wantErr bool
		want    int64
	}{
		{"valid", "1048576", false, 1048576},
		{"not an integer", "big", true, 0},
		{"below minimum", "512", true, 0},
		{"at minimum", "1024", false, 1024},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("MAX_BODY_BYTES", tc.val)
			c, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.val)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.MaxBodyBytes != tc.want {
				t.Errorf("MaxBodyBytes = %d, want %d", c.MaxBodyBytes, tc.want)
			}
		})
	}
}

func TestLoadWorkerCount(t *testing.T) {
	tests := []struct {
		name    string
		val     string
		wantErr bool
		want    int
	}{
		{"valid", "16", false, 16},
		{"not an integer", "many", true, 0},
		{"zero out of range", "0", true, 0},
		{"above range", "65", true, 0},
		{"at max", "64", false, 64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("WORKER_COUNT", tc.val)
			c, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.val)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.WorkerCount != tc.want {
				t.Errorf("WorkerCount = %d, want %d", c.WorkerCount, tc.want)
			}
		})
	}
}

func TestLoadShadowMode(t *testing.T) {
	tests := []struct {
		name    string
		val     string
		wantErr bool
		want    bool
	}{
		{"true", "true", false, true},
		{"one", "1", false, true},
		{"false", "false", false, false},
		{"not a bool", "yes-please", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("SHADOW_MODE", tc.val)
			c, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.val)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.ShadowMode != tc.want {
				t.Errorf("ShadowMode = %v, want %v", c.ShadowMode, tc.want)
			}
		})
	}
}

func TestLoadModelThreads(t *testing.T) {
	tests := []struct {
		name    string
		val     string
		wantErr bool
		want    int
	}{
		{"valid", "8", false, 8},
		{"not an integer", "lots", true, 0},
		{"zero out of range", "0", true, 0},
		{"above range", "65", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("MODEL_THREADS", tc.val)
			c, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.val)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.ModelThreads != tc.want {
				t.Errorf("ModelThreads = %d, want %d", c.ModelThreads, tc.want)
			}
		})
	}
}

func TestLoadGitHubMaxRetries(t *testing.T) {
	tests := []struct {
		name    string
		val     string
		wantErr bool
		want    int
	}{
		{"valid", "3", false, 3},
		{"zero allowed", "0", false, 0},
		{"not an integer", "retry", true, 0},
		{"above range", "6", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("GITHUB_MAX_RETRIES", tc.val)
			c, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.val)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.GitHubMaxRetries != tc.want {
				t.Errorf("GitHubMaxRetries = %d, want %d", c.GitHubMaxRetries, tc.want)
			}
		})
	}
}
