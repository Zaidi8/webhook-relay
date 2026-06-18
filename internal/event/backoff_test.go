package event

import (
	"testing"
)

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
		got := backoffSeconds(c.attempts)
		if got != c.want {
			t.Errorf("backoff for attempts=%d: got %d, want %d", c.attempts, got, c.want)
		}
	}
}
