package hub

import (
	"net/http"
	"net/http/httptest"
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

func TestOriginChecker(t *testing.T) {
	reqWithOrigin := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}

	// No allowlist: everything is accepted.
	allowAny := originChecker(nil)
	if !allowAny(reqWithOrigin("https://evil.example")) {
		t.Fatal("empty allowlist should accept any origin")
	}

	// With an allowlist: only listed browser http(s) origins (case/trailing-slash
	// insensitive) pass. Native/desktop origins (none, "null", non-web scheme) are
	// never gated.
	check := originChecker([]string{"https://app.example.com/", " HTTPS://Other.Example "})
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://app.example.com", true},
		{"https://app.example.com/", true},
		{"https://other.example", true},
		{"", true},              // native/CLI client, no Origin header
		{"null", true},          // sandboxed/opaque origin
		{"file://", true},       // desktop app loaded from disk (Electron)
		{"app://feishin", true}, // custom app scheme
		{"https://evil.example", false},
		{"http://app.example.com", false}, // scheme mismatch (http vs https)
	}
	for _, tc := range cases {
		if got := check(reqWithOrigin(tc.origin)); got != tc.want {
			t.Errorf("origin %q: got %v, want %v", tc.origin, got, tc.want)
		}
	}
}
