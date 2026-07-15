// Package verify implements SYSTEM_FLOW stage 1: constant-time HMAC-SHA256
// verification of GitHub's X-Hub-Signature-256 header. Requests that fail
// verification are dropped with 401 before any payload is parsed.
package verify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
)

const signatureHeader = "X-Hub-Signature-256"

// Middleware wraps next, admitting only requests whose body carries a valid
// signature for secret. The verified body is restored so next can read it.
func Middleware(secret string, next http.Handler) http.Handler {
	key := []byte(secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read body", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()

		if !valid(key, body, r.Header.Get(signatureHeader)) {
			http.Error(w, "signature verification failed", http.StatusUnauthorized)
			return
		}

		// Restore the body for the downstream handler.
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

// valid reports whether received matches HMAC-SHA256(body, key), compared in
// constant time to avoid leaking the digest via timing.
func valid(key, body []byte, received string) bool {
	if received == "" {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(received))
}
