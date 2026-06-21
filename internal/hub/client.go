package hub

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/alsoGAMER/listen-together/internal/protocol"
)

// client is a single WebSocket connection. Only writePump writes to the socket;
// all other goroutines enqueue onto sendCh.
type client struct {
	hub  *Hub
	conn *websocket.Conn

	id       string // member id (stable for the connection's lifetime)
	username string
	server   string // normalized server URL bound at authentication
	authed   bool

	// Auth throttling state. Only ever touched from the readPump goroutine (via
	// dispatch -> handleAuthenticate), so it needs no lock.
	authFailures int
	lastAuthAt   time.Time

	sendCh    chan []byte
	done      chan struct{}
	closeOnce sync.Once

	mu     sync.Mutex
	roomID string
}

func (c *client) setRoom(id string) {
	c.mu.Lock()
	c.roomID = id
	c.mu.Unlock()
}

func (c *client) currentRoom() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.roomID
}

func (c *client) close() {
	c.closeOnce.Do(func() { close(c.done) })
}

// authBackoff reports how long the client must wait before another auth attempt,
// based on consecutive failures (exponential, capped). Zero means it may try now.
// This stops a connection from using the server to hammer arbitrary Subsonic
// endpoints, since each attempt triggers an outbound ping.
func (c *client) authBackoff() time.Duration {
	if c.authFailures == 0 {
		return 0
	}
	shift := c.authFailures - 1
	if shift > maxAuthBackoffShift {
		shift = maxAuthBackoffShift
	}
	backoff := authBackoffBase << shift
	elapsed := time.Since(c.lastAuthAt)
	if elapsed >= backoff {
		return 0
	}
	return backoff - elapsed
}

// send marshals an envelope and queues it. Non-blocking: if the client's buffer
// is full the message is dropped (the next roomState re-syncs state).
func (c *client) send(event string, data interface{}) {
	raw, err := json.Marshal(data)
	if err != nil {
		log.Printf("marshal %s: %v", event, err)
		return
	}
	env, err := json.Marshal(protocol.Envelope{Event: event, Data: raw})
	if err != nil {
		log.Printf("marshal envelope %s: %v", event, err)
		return
	}
	select {
	case c.sendCh <- env:
	case <-c.done:
	default:
		log.Printf("dropping %s to slow client %s", event, c.id)
	}
}

func (c *client) sendError(msg string) {
	c.send(protocol.EvError, protocol.ErrorPayload{Message: msg})
}

func (c *client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case msg := <-c.sendCh:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		}
	}
}

func (c *client) readPump() {
	defer func() {
		c.hub.handleDisconnect(c)
		c.close()
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	// Until authenticated the connection has only a short deadline, so an idle or
	// unauthenticated socket can't linger (handleAuthenticate extends it on
	// success). Pre-auth we don't let pongs reset it, or a client could stay
	// connected forever without ever authenticating.
	c.conn.SetReadDeadline(time.Now().Add(c.hub.authTimeout))
	c.conn.SetPongHandler(func(string) error {
		if c.authed {
			c.conn.SetReadDeadline(time.Now().Add(pongWait))
		}
		return nil
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			c.sendError("malformed message")
			continue
		}
		c.hub.dispatch(c, env)
	}
}
