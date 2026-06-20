package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alsoGAMER/listen-together/internal/protocol"
)

func TestNormalizeServerURL(t *testing.T) {
	cases := map[string]string{
		"https://music.example.com/":    "https://music.example.com",
		"https://music.example.com/nd/": "https://music.example.com/nd",
		"  http://localhost:4533  ":     "http://localhost:4533",
		"https://x.com/?foo=bar#frag":   "https://x.com",
		"not a url":                     "",
		"":                              "",
	}
	for in, want := range cases {
		if got := NormalizeServerURL(in); got != want {
			t.Errorf("NormalizeServerURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// mockSubsonic returns a server that answers /rest/ping.view with the given status.
func mockSubsonic(t *testing.T, status string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/ping.view" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if status == "ok" {
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok","version":"1.16.1"}}`))
		} else {
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"failed","error":{"code":40,"message":"Wrong username or password"}}}`))
		}
	}))
}

func TestValidateSuccessAndCache(t *testing.T) {
	srv := mockSubsonic(t, "ok")
	defer srv.Close()

	a := New(nil) // allow any
	server, err := a.Validate(context.Background(), protocol.AuthenticatePayload{
		ServerURL: srv.URL, Username: "alice", Token: "abc", Salt: "xyz",
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if server != NormalizeServerURL(srv.URL) {
		t.Fatalf("server = %q", server)
	}

	// Second call should hit the cache (still succeeds even though server is down).
	srv.Close()
	if _, err := a.Validate(context.Background(), protocol.AuthenticatePayload{
		ServerURL: server, Username: "alice", Token: "abc", Salt: "xyz",
	}); err != nil {
		t.Fatalf("cached Validate: %v", err)
	}
}

func TestValidateFailure(t *testing.T) {
	srv := mockSubsonic(t, "failed")
	defer srv.Close()

	if _, err := New(nil).Validate(context.Background(), protocol.AuthenticatePayload{
		ServerURL: srv.URL, Username: "alice", Token: "bad", Salt: "xyz",
	}); err == nil {
		t.Fatal("expected auth failure")
	}
}

func TestValidateAllowlist(t *testing.T) {
	srv := mockSubsonic(t, "ok")
	defer srv.Close()

	a := New([]string{"https://only-this.example.com"})
	if _, err := a.Validate(context.Background(), protocol.AuthenticatePayload{
		ServerURL: srv.URL, Username: "alice", Token: "abc", Salt: "xyz",
	}); err == nil {
		t.Fatal("expected rejection of non-allowlisted server")
	}
}

func TestValidateMissingFields(t *testing.T) {
	if _, err := New(nil).Validate(context.Background(), protocol.AuthenticatePayload{
		ServerURL: "https://x.com", Username: "a",
	}); err == nil {
		t.Fatal("expected error for missing credentials")
	}
}
