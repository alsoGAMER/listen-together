// Command listen-together runs the synchronized-playback coordinator: a small
// WebSocket server that lets multiple Subsonic/Navidrome clients listen in sync.
//
// Configuration (environment):
//
//	LT_PORT                  HTTP/WS listen port (default 4040)
//	LT_ALLOWED_SERVERS       comma-separated allowlist of server base URLs. If
//	                         empty, any Subsonic server is accepted (open relay).
//	LT_ALLOWED_ORIGINS       comma-separated allowlist of browser http(s) origins
//	                         for the WS upgrade. If empty, any origin is accepted.
//	                         Only http(s) origins are gated; native/desktop clients
//	                         (no Origin, "null", file://, …) are always allowed.
//	LT_MAX_ROOMS             cap on concurrent rooms (default 0 = unlimited).
//	LT_MAX_MEMBERS_PER_ROOM  cap on members per room (default 0 = unlimited).
//	LT_STATS_TOKEN           if set, enables GET /stats, protected by this bearer
//	                         token (?token= or "Authorization: Bearer"). If empty
//	                         the endpoint is not registered.
//
// Endpoints: GET /ws (WebSocket), GET /healthz, and GET /stats when enabled.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alsoGAMER/listen-together/internal/auth"
	"github.com/alsoGAMER/listen-together/internal/hub"
)

func main() {
	port := getenv("LT_PORT", "4040")
	allowed := splitAndTrim(os.Getenv("LT_ALLOWED_SERVERS"))
	opts := hub.Options{
		MaxRooms:          getenvInt("LT_MAX_ROOMS", 0),
		MaxMembersPerRoom: getenvInt("LT_MAX_MEMBERS_PER_ROOM", 0),
		AllowedOrigins:    splitAndTrim(os.Getenv("LT_ALLOWED_ORIGINS")),
	}

	h := hub.New(auth.New(allowed), opts)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.ServeWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	if statsToken := strings.TrimSpace(os.Getenv("LT_STATS_TOKEN")); statsToken != "" {
		mux.HandleFunc("/stats", requireToken(statsToken, h.ServeStats))
		log.Printf("/stats enabled (token-protected)")
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	if len(allowed) == 0 {
		log.Printf("WARNING: no LT_ALLOWED_SERVERS set; accepting any Subsonic server (open relay)")
	} else {
		log.Printf("allowed servers: %s", strings.Join(allowed, ", "))
	}
	if opts.MaxRooms > 0 || opts.MaxMembersPerRoom > 0 {
		log.Printf("limits: max rooms=%d, max members/room=%d (0=unlimited)", opts.MaxRooms, opts.MaxMembersPerRoom)
	}
	if len(opts.AllowedOrigins) > 0 {
		log.Printf("allowed origins: %s", strings.Join(opts.AllowedOrigins, ", "))
	}

	go func() {
		log.Printf("listening on :%s (ws at /ws)", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	h.Shutdown() // close hijacked WebSocket connections srv.Shutdown leaves open
	log.Printf("shut down")
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("invalid %s=%q (want non-negative int); using %d", key, v, def)
		return def
	}
	return n
}

// requireToken wraps a handler with a constant-time bearer-token check. The token
// may be supplied as ?token= or an "Authorization: Bearer <token>" header.
func requireToken(token string, next http.HandlerFunc) http.HandlerFunc {
	want := []byte(token)
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("token")
		if got == "" {
			got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func splitAndTrim(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
