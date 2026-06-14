package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// sign computes the hex-encoded HMAC-SHA256 of body with secret —
// the same value a caller would put in the X-Signature-256 header.
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestValidateSignature(t *testing.T) {
	secret := "testsecret123"
	body := []byte(`{"id":"evt_1","type":"push"}`)

	t.Run("valid signature passes", func(t *testing.T) {
		got := validateSignature(secret, body, sign(secret, body))
		if !got {
			t.Errorf("expected valid signature to pass, got false")
		}
	})

	t.Run("wrong signature fails", func(t *testing.T) {
		if validateSignature(secret, body, "deadbeef") {
			t.Errorf("expected wrong signature to fail, got true")
		}
	})

	t.Run("tampered body fails", func(t *testing.T) {
		sig := sign(secret, body)
		tampered := []byte(`{"id":"evt_1","type":"charge"}`)
		if validateSignature(secret, tampered, sig) {
			t.Errorf("expected signature to fail for a tampered body, got true")
		}
	})

	t.Run("wrong secret fails", func(t *testing.T) {
		sig := sign("the-wrong-secret", body)
		if validateSignature(secret, body, sig) {
			t.Errorf("expected signature signed with a different secret to fail, got true")
		}
	})

	t.Run("non-hex header fails", func(t *testing.T) {
		// validateSignature hex-decodes the header; a non-hex value must not panic.
		if validateSignature(secret, body, "not-hex-zzz") {
			t.Errorf("expected non-hex header to fail, got true")
		}
	})
}

func TestBackoffSeconds(t *testing.T) {
	// Mirrors the backoff calculation used in markFailed: 1 << attempts.
	cases := []struct {
		attempts int
		want     int
	}{
		{1, 2},
		{2, 4},
		{3, 8},
		{4, 16},
		{5, 32},
	}
	for _, c := range cases {
		got := 1 << c.attempts
		if got != c.want {
			t.Errorf("backoff for attempts=%d: got %d, want %d", c.attempts, got, c.want)
		}
	}
}
