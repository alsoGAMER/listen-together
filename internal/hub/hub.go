// Package hub is the WebSocket transport layer. It upgrades connections, tracks
// live clients, authenticates them, routes inbound events, and broadcasts room
// state. Session semantics live in the room package; this package only moves
// bytes and maps member ids back to live connections.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/alsoGAMER/listen-together/internal/auth"
	"github.com/alsoGAMER/listen-together/internal/protocol"
	"github.com/alsoGAMER/listen-together/internal/room"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 1 << 16
	sendBuffer     = 64

	// Auth throttling: each failed attempt grows the required wait exponentially
	// (authBackoffBase << failures-1, capped), and a connection is dropped once it
	// reaches maxAuthFailures. This bounds outbound pings a client can trigger.
	maxAuthFailures     = 10
	authBackoffBase     = 500 * time.Millisecond
	maxAuthBackoffShift = 6 // cap backoff at authBackoffBase<<6 = 32s
)

// Options configures a Hub. The zero value is valid: unlimited rooms/members and
// no Origin gating (the historical default — per-message Subsonic auth is the
// real guard).
type Options struct {
	MaxRooms          int      // cap on concurrent rooms; 0 = unlimited
	MaxMembersPerRoom int      // cap on members per room; 0 = unlimited
	AllowedOrigins    []string // browser Origin allowlist; empty = allow any
}

// Hub ties together the room manager, the authenticator, and the live clients.
type Hub struct {
	rooms    *room.Manager
	auth     *auth.Authenticator
	upgrader websocket.Upgrader

	mu      sync.Mutex
	clients map[string]*client // memberID -> client
}

// New builds a Hub backed by the given authenticator and options.
func New(a *auth.Authenticator, opts Options) *Hub {
	return &Hub{
		rooms:   room.New(opts.MaxRooms, opts.MaxMembersPerRoom),
		auth:    a,
		clients: make(map[string]*client),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     originChecker(opts.AllowedOrigins),
		},
	}
}

// originChecker returns a CheckOrigin func. With no allowlist it accepts any
// origin (browser clients connect cross-origin and auth is per-message). With an
// allowlist it accepts only those origins; requests with no Origin header (native
// or CLI clients, which aren't subject to browser CSWSH) are also accepted.
func originChecker(allowedOrigins []string) func(*http.Request) bool {
	if len(allowedOrigins) == 0 {
		return func(*http.Request) bool { return true }
	}
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o = strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/")); o != "" {
			allowed[o] = struct{}{}
		}
	}
	return func(r *http.Request) bool {
		origin := strings.ToLower(strings.TrimRight(r.Header.Get("Origin"), "/"))
		if origin == "" {
			return true
		}
		_, ok := allowed[origin]
		return ok
	}
}

// ServeWS upgrades an HTTP request to a WebSocket connection and starts its pumps.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrader already wrote an error response
	}
	c := &client{
		hub:    h,
		conn:   conn,
		id:     newMemberID(),
		sendCh: make(chan []byte, sendBuffer),
		done:   make(chan struct{}),
	}
	h.addClient(c)
	go c.writePump()
	go c.readPump()
}

// Shutdown closes all live client connections. The HTTP server's own Shutdown
// does not touch hijacked WebSocket connections, so the process calls this after
// it to tear them down cleanly instead of dropping them on exit.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	clients := make([]*client, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.close()          // signal writePump to send a close frame
		_ = c.conn.Close() // unblock readPump so it tears down and leaves its room
	}
}

// Stats is a point-in-time snapshot of server load.
type Stats struct {
	Rooms   int `json:"rooms"`
	Members int `json:"members"`
	Clients int `json:"clients"`
}

// Stats reports current room/member/client counts.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	clients := len(h.clients)
	h.mu.Unlock()
	rooms, members := h.rooms.Counts()
	return Stats{Rooms: rooms, Members: members, Clients: clients}
}

// ServeStats writes the current Stats as JSON. Useful for monitoring a
// self-hosted instance (e.g. scraping or a status check).
func (h *Hub) ServeStats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.Stats())
}

func (h *Hub) addClient(c *client) {
	h.mu.Lock()
	h.clients[c.id] = c
	h.mu.Unlock()
}

func (h *Hub) removeClient(id string) {
	h.mu.Lock()
	delete(h.clients, id)
	h.mu.Unlock()
}

func (h *Hub) getClient(id string) *client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clients[id]
}

// dispatch routes a single inbound envelope.
func (h *Hub) dispatch(c *client, env protocol.Envelope) {
	if env.Event == protocol.EvAuthenticate {
		h.handleAuthenticate(c, env.Data)
		return
	}
	if !c.authed {
		c.sendError("not authenticated")
		return
	}
	switch env.Event {
	case protocol.EvCreateRoom:
		h.handleCreateRoom(c)
	case protocol.EvJoinRoom:
		h.handleJoinRoom(c, env.Data)
	case protocol.EvLeaveRoom:
		h.leaveAndBroadcast(c)
	case protocol.EvTransport:
		h.handleTransport(c, env.Data)
	case protocol.EvRequestControl:
		h.handleRequestControl(c)
	case protocol.EvPassControl:
		h.handlePassControl(c, env.Data)
	case protocol.EvPing:
		h.handlePing(c, env.Data)
	default:
		c.sendError("unknown event: " + env.Event)
	}
}

func (h *Hub) handleAuthenticate(c *client, raw json.RawMessage) {
	// Throttle repeated attempts so a connection can't use us to hammer arbitrary
	// Subsonic endpoints (each attempt below triggers an outbound ping).
	if c.authBackoff() > 0 {
		c.sendError("too many authentication attempts; slow down")
		return
	}
	c.lastAuthAt = time.Now()

	var p protocol.AuthenticatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		c.sendError("invalid authenticate payload")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server, err := h.auth.Validate(ctx, p)
	if err != nil {
		c.authFailures++
		c.sendError("authentication failed: " + err.Error())
		if c.authFailures >= maxAuthFailures {
			_ = c.conn.Close() // readPump unblocks, tears the connection down
		}
		return
	}
	c.authFailures = 0
	c.server = server
	c.username = p.Username
	c.authed = true
	c.send(protocol.EvAuthenticated, protocol.AuthenticatedPayload{MemberID: c.id, Username: c.username})
}

func (h *Hub) handleCreateRoom(c *client) {
	if c.currentRoom() != "" {
		h.leaveAndBroadcast(c)
	}
	r, err := h.rooms.Create(c.id, c.username)
	if err != nil {
		c.sendError(err.Error())
		return
	}
	c.setRoom(r.ID())
	h.broadcastRoomState(r)
}

func (h *Hub) handleJoinRoom(c *client, raw json.RawMessage) {
	var p protocol.JoinRoomPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.RoomID == "" {
		c.sendError("invalid joinRoom payload")
		return
	}
	if c.currentRoom() != "" {
		h.leaveAndBroadcast(c)
	}
	r, err := h.rooms.Join(p.RoomID, c.id, c.username)
	if err != nil {
		c.sendError(err.Error())
		return
	}
	c.setRoom(r.ID())
	h.broadcastRoomState(r)
}

func (h *Hub) handleTransport(c *client, raw json.RawMessage) {
	r, ok := h.rooms.Get(c.currentRoom())
	if !ok {
		c.sendError("not in a room")
		return
	}
	var in protocol.TransportInput
	if err := json.Unmarshal(raw, &in); err != nil {
		c.sendError("invalid transport payload")
		return
	}
	if !r.ApplyTransport(c.id, in) {
		return // not the host: ignore silently
	}
	h.broadcastRoomState(r)
}

func (h *Hub) handleRequestControl(c *client) {
	r, ok := h.rooms.Get(c.currentRoom())
	if !ok {
		c.sendError("not in a room")
		return
	}
	if host := h.getClient(r.HostID()); host != nil {
		host.send(protocol.EvControlRequested, protocol.ControlRequestedPayload{
			FromMemberID: c.id, FromUsername: c.username,
		})
	}
}

func (h *Hub) handlePassControl(c *client, raw json.RawMessage) {
	r, ok := h.rooms.Get(c.currentRoom())
	if !ok {
		c.sendError("not in a room")
		return
	}
	var p protocol.PassControlPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.ToMemberID == "" {
		c.sendError("invalid passControl payload")
		return
	}
	if !r.PassControl(c.id, p.ToMemberID) {
		c.sendError("cannot pass control")
		return
	}
	h.broadcastRoomState(r)
}

func (h *Hub) handlePing(c *client, raw json.RawMessage) {
	var p protocol.PingPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		c.sendError("invalid ping payload")
		return
	}
	c.send(protocol.EvPong, protocol.PongPayload{T0: p.T0, ServerTimeMs: time.Now().UnixMilli()})
}

func (h *Hub) handleDisconnect(c *client) {
	h.leaveAndBroadcast(c)
	h.removeClient(c.id)
}

// leaveAndBroadcast removes c from its room and notifies remaining members.
func (h *Hub) leaveAndBroadcast(c *client) {
	roomID := c.currentRoom()
	if roomID == "" {
		return
	}
	c.setRoom("")
	r, deleted, _ := h.rooms.Leave(roomID, c.id)
	if r == nil || deleted {
		return
	}
	h.broadcastRoomState(r)
}

func (h *Hub) broadcastRoomState(r *room.Room) {
	snap := r.Snapshot()
	for _, id := range r.MemberIDs() {
		if c := h.getClient(id); c != nil {
			c.send(protocol.EvRoomState, snap)
		}
	}
}

func newMemberID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func init() { log.SetPrefix("listen-together ") }
