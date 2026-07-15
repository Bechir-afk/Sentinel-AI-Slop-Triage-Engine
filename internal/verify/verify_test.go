package verify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSecret = "s3cr3t"

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// spy records whether the wrapped handler ran and what body it saw.
type spy struct {
	called bool
	body   string
}

func (s *spy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.called = true
	b, _ := io.ReadAll(r.Body)
	s.body = string(b)
	w.WriteHeader(http.StatusOK)
}

func TestMiddleware(t *testing.T) {
	const body = `{"action":"opened"}`

	tests := []struct {
		name       string
		signature  string
		wantStatus int
		wantCalled bool
	}{
		{"valid signature passes", sign(testSecret, body), http.StatusOK, true},
		{"invalid signature rejected", sign("wrong-secret", body), http.StatusUnauthorized, false},
		{"missing signature rejected", "", http.StatusUnauthorized, false},
		{"garbage signature rejected", "sha256=notavalidhex", http.StatusUnauthorized, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next := &spy{}
			h := Middleware(testSecret, next)

			req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
			if tc.signature != "" {
				req.Header.Set(signatureHeader, tc.signature)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if next.called != tc.wantCalled {
				t.Errorf("next called = %v, want %v", next.called, tc.wantCalled)
			}
			if tc.wantCalled && next.body != body {
				t.Errorf("downstream body = %q, want %q (body not restored)", next.body, body)
			}
		})
	}
}
