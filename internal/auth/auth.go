// Package auth validates listen-together clients by performing a Subsonic ping
// against the user's own server. This makes the server work with Navidrome and
// any other Subsonic-compatible server, reusing the user's existing account.
// Credentials are validated only against the user's own server and are never
// stored beyond a short-lived success cache.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/alsoGAMER/listen-together/internal/protocol"
)

// Authenticator validates credentials and caches recent successes.
type Authenticator struct {
	httpClient *http.Client
	allowed    []string // normalized allowlist of server base URLs; empty = allow any

	mu        sync.Mutex
	cache     map[string]time.Time // credential fingerprint -> expiry
	ttl       time.Duration
	lastSweep time.Time // last time expired entries were purged
}

// New builds an Authenticator. If allowedServers is empty, any Subsonic server
// is accepted (open relay — convenient for local use, not recommended in prod).
func New(allowedServers []string) *Authenticator {
	var allowed []string
	for _, s := range allowedServers {
		if s = NormalizeServerURL(s); s != "" {
			allowed = append(allowed, s)
		}
	}
	return &Authenticator{
		httpClient: &http.Client{Timeout: 8 * time.Second},
		allowed:    allowed,
		cache:      make(map[string]time.Time),
		ttl:        5 * time.Minute,
	}
}

// subsonicResponse is the minimal envelope we need from a ping.
type subsonicResponse struct {
	SubsonicResponse struct {
		Status string `json:"status"`
		Error  struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"subsonic-response"`
}

// Validate checks the credentials in p. On success it returns the normalized
// server URL to bind to the connection.
func (a *Authenticator) Validate(ctx context.Context, p protocol.AuthenticatePayload) (string, error) {
	server := NormalizeServerURL(p.ServerURL)
	switch {
	case server == "":
		return "", fmt.Errorf("missing or invalid serverUrl")
	case p.Username == "":
		return "", fmt.Errorf("missing username")
	case (p.Token == "" || p.Salt == "") && p.Password == "":
		return "", fmt.Errorf("missing credentials (need token+salt or password)")
	case !a.serverAllowed(server):
		return "", fmt.Errorf("server not allowed: %s", server)
	}

	fp := credFingerprint(server, p)
	if a.cachedValid(fp) {
		return server, nil
	}
	if err := a.pingSubsonic(ctx, server, p); err != nil {
		return "", err
	}
	a.storeValid(fp)
	return server, nil
}

func (a *Authenticator) pingSubsonic(ctx context.Context, server string, p protocol.AuthenticatePayload) error {
	q := url.Values{}
	q.Set("u", p.Username)
	q.Set("v", "1.16.1")
	q.Set("c", "listen-together")
	q.Set("f", "json")
	if p.Token != "" && p.Salt != "" {
		q.Set("t", p.Token)
		q.Set("s", p.Salt)
	} else {
		q.Set("p", p.Password)
	}

	endpoint := server + "/rest/ping.view?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ping request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return fmt.Errorf("reading ping response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping returned HTTP %d", resp.StatusCode)
	}

	var sr subsonicResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return fmt.Errorf("invalid ping response: %w", err)
	}
	if sr.SubsonicResponse.Status != "ok" {
		msg := sr.SubsonicResponse.Error.Message
		if msg == "" {
			msg = "authentication failed"
		}
		return fmt.Errorf("subsonic ping failed: %s", msg)
	}
	return nil
}

func (a *Authenticator) serverAllowed(server string) bool {
	if len(a.allowed) == 0 {
		return true
	}
	for _, s := range a.allowed {
		if s == server {
			return true
		}
	}
	return false
}

func (a *Authenticator) cachedValid(fp string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.cache[fp]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(a.cache, fp)
		return false
	}
	return true
}

func (a *Authenticator) storeValid(fp string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	// Opportunistically purge expired entries (at most once per ttl) so the cache
	// can't grow unbounded over long uptimes with many distinct credentials.
	if now.Sub(a.lastSweep) >= a.ttl {
		for k, exp := range a.cache {
			if now.After(exp) {
				delete(a.cache, k)
			}
		}
		a.lastSweep = now
	}
	a.cache[fp] = now.Add(a.ttl)
}

func credFingerprint(server string, p protocol.AuthenticatePayload) string {
	h := sha256.Sum256([]byte(server + "\x00" + p.Username + "\x00" + p.Token + "\x00" + p.Salt + "\x00" + p.Password))
	return hex.EncodeToString(h[:])
}

// NormalizeServerURL trims, validates, and canonicalizes a server base URL
// (drops trailing slash, query, and fragment). Returns "" if invalid.
func NormalizeServerURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
