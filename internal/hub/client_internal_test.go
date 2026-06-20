package hub

import (
	"testing"
	"time"
)

func TestAuthBackoff(t *testing.T) {
	c := &client{}

	// No failures yet: may try immediately.
	if d := c.authBackoff(); d != 0 {
		t.Fatalf("backoff with 0 failures = %v, want 0", d)
	}

	// One failure that just happened: must wait roughly the base interval.
	c.authFailures = 1
	c.lastAuthAt = time.Now()
	if d := c.authBackoff(); d <= 0 || d > authBackoffBase {
		t.Fatalf("backoff after 1 fresh failure = %v, want (0, %v]", d, authBackoffBase)
	}

	// Enough time elapsed: the window has passed.
	c.lastAuthAt = time.Now().Add(-authBackoffBase - time.Millisecond)
	if d := c.authBackoff(); d != 0 {
		t.Fatalf("backoff after window elapsed = %v, want 0", d)
	}

	// Backoff grows with failures but is capped at authBackoffBase<<maxAuthBackoffShift.
	c.authFailures = maxAuthFailures
	c.lastAuthAt = time.Now()
	capped := authBackoffBase << maxAuthBackoffShift
	if d := c.authBackoff(); d <= 0 || d > capped {
		t.Fatalf("capped backoff = %v, want (0, %v]", d, capped)
	}
}
