// Command listen-together runs the synchronized-playback coordinator: a small
// WebSocket server that lets multiple Subsonic/Navidrome clients listen in sync.
//
// Configuration (environment):
//
//	LT_PORT             HTTP/WS listen port (default 4040)
//	LT_ALLOWED_SERVERS  comma-separated allowlist of server base URLs. If empty,
//	                    any Subsonic server is accepted (open relay).
//
// Endpoints: GET /ws (WebSocket), GET /healthz.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alsoGAMER/listen-together/internal/auth"
	"github.com/alsoGAMER/listen-together/internal/hub"
)

func main() {
	port := getenv("LT_PORT", "4040")
	allowed := splitAndTrim(os.Getenv("LT_ALLOWED_SERVERS"))

	h := hub.New(auth.New(allowed))

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.ServeWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

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
	log.Printf("shut down")
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
