// Entity: Signed Delivery (003 spec Key Entities) — the HMAC-SHA256 signer and
// request builder that make a delivery indistinguishable from a genuine GitHub
// webhook to the shipped gateway.
//
// The signature is computed over the exact raw bytes the gateway will read
// (fixtures.go hands out []byte, not a re-marshaled struct), so the trust
// boundary is exercised on real bytes (T004). A badSign variant produces a
// mismatched signature for the US2 401 drill.
package integration

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

// sign computes the GitHub X-Hub-Signature-256 header value for a body:
// "sha256=" + hex(HMAC-SHA256(secret, body)) — mirroring internal/verify/verify.go.
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// signedRequest builds a POST /webhook carrying a genuine signature over body,
// plus the delivery ID and event type headers the gateway gates on. It targets
// baseURL (the booted gateway) and signs with secret (the configured
// GITHUB_WEBHOOK_SECRET).
func signedRequest(baseURL, secret, event, deliveryID string, body []byte) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, baseURL+"/webhook", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", sign(secret, body))
	return req, nil
}

// badSignedRequest builds the same request but with a signature that does not
// match the body (US2 401 drill): the gateway must reject it before any
// downstream call. We sign a different byte to guarantee a mismatch rather than
// hard-coding a hex string that a future encoding change could accidentally
// make valid.
func badSignedRequest(baseURL, secret, event, deliveryID string, body []byte) (*http.Request, error) {
	req, err := signedRequest(baseURL, secret, event, deliveryID, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Hub-Signature-256", sign(secret, append([]byte{'x'}, body...)))
	return req, nil
}
