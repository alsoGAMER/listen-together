// Package room holds the ephemeral, in-memory state of listening sessions:
// membership, the single host that owns transport authority, and the
// authoritative playback Transport. It is deliberately transport-agnostic — it
// knows nothing about WebSockets or clients, only member ids — so it is easy to
// test and reason about. The hub maps member ids back to live connections.
package room

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/alsoGAMER/listen-together/internal/protocol"
)

// Room is a single listening session. All state lives in memory.
type Room struct {
	mu sync.Mutex

	id        string
	hostID    string
	members   map[string]string // memberID -> username
	transport protocol.Transport
	seq       uint64

	// lastClientTimeMs is the highest host-stamped logical clock applied so far.
	// It guards against out-of-order transports within a single host's tenure and
	// is reset to 0 whenever the host changes (a new host's clock is unrelated).
	lastClientTimeMs int64
}

// ID returns the room's immutable shareable code.
func (r *Room) ID() string { return r.id }

// HostID returns the current host's member id.
func (r *Room) HostID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hostID
}

// MemberIDs returns a snapshot of current member ids.
func (r *Room) MemberIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.members))
	for id := range r.members {
		ids = append(ids, id)
	}
	return ids
}

// Snapshot returns a copy of the room state safe to send to clients.
func (r *Room) Snapshot() protocol.RoomStatePayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	members := make([]protocol.Member, 0, len(r.members))
	for id, name := range r.members {
		members = append(members, protocol.Member{ID: id, Username: name})
	}
	t := r.transport
	if t.Queue == nil {
		t.Queue = []string{}
	}
	return protocol.RoomStatePayload{
		RoomID:       r.id,
		HostMemberID: r.hostID,
		Members:      members,
		Seq:          r.seq,
		Transport:    t,
	}
}

// ApplyTransport updates the room's transport if memberID is the host. Returns
// false (input ignored) otherwise.
func (r *Room) ApplyTransport(memberID string, in protocol.TransportInput) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hostID != memberID {
		return false
	}
	// Drop transports that arrive out of order (stale logical clock). A zero
	// ClientTimeMs means the client doesn't stamp one, so the guard is skipped.
	if in.ClientTimeMs != 0 {
		if in.ClientTimeMs < r.lastClientTimeMs {
			return false
		}
		r.lastClientTimeMs = in.ClientTimeMs
	}
	q := in.Queue
	if q == nil {
		q = []string{}
	}
	r.transport = protocol.Transport{
		Playing:      in.Playing,
		PositionMs:   in.PositionMs,
		TrackID:      in.TrackID,
		Queue:        q,
		QueueIndex:   in.QueueIndex,
		ServerTimeMs: nowMs(),
	}
	r.seq++
	return true
}

// PassControl transfers host from fromID to toID, requiring fromID to be the
// current host and toID to be a member. Returns true on success.
func (r *Room) PassControl(fromID, toID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hostID != fromID {
		return false
	}
	if _, ok := r.members[toID]; !ok {
		return false
	}
	r.hostID = toID
	r.lastClientTimeMs = 0 // new host, unrelated clock
	r.seq++
	return true
}

// Manager owns the set of live rooms.
type Manager struct {
	mu    sync.Mutex
	rooms map[string]*Room
}

// New returns an empty Manager.
func New() *Manager {
	return &Manager{rooms: make(map[string]*Room)}
}

// Get returns the room with the given id, if any.
func (m *Manager) Get(roomID string) (*Room, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	return r, ok
}

// Create makes a new room with the given member as its sole member and host.
func (m *Manager) Create(hostID, hostUsername string) *Room {
	m.mu.Lock()
	defer m.mu.Unlock()
	code := m.uniqueCodeLocked()
	r := &Room{
		id:        code,
		hostID:    hostID,
		members:   map[string]string{hostID: hostUsername},
		transport: protocol.Transport{Queue: []string{}, QueueIndex: -1, ServerTimeMs: nowMs()},
	}
	m.rooms[code] = r
	return r
}

// Join adds a member to an existing room.
func (m *Manager) Join(roomID, memberID, username string) (*Room, error) {
	r, ok := m.Get(roomID)
	if !ok {
		return nil, fmt.Errorf("room not found: %s", roomID)
	}
	r.mu.Lock()
	r.members[memberID] = username
	r.mu.Unlock()
	return r, nil
}

// Leave removes a member from a room. It returns the affected room (for
// re-broadcast), whether the room was deleted (became empty), and whether the
// host was reassigned.
func (m *Manager) Leave(roomID, memberID string) (r *Room, deleted, hostChanged bool) {
	r, ok := m.Get(roomID)
	if !ok {
		return nil, false, false
	}

	r.mu.Lock()
	delete(r.members, memberID)
	if len(r.members) == 0 {
		r.mu.Unlock()
		m.mu.Lock()
		delete(m.rooms, roomID)
		m.mu.Unlock()
		return r, true, false
	}
	if r.hostID == memberID {
		for id := range r.members { // reassign to an arbitrary remaining member
			r.hostID = id
			break
		}
		r.lastClientTimeMs = 0 // new host, unrelated clock
		hostChanged = true
	}
	r.mu.Unlock()
	return r, false, hostChanged
}

// uniqueCodeLocked must be called with m.mu held.
func (m *Manager) uniqueCodeLocked() string {
	for {
		code := randomCode(6)
		if _, exists := m.rooms[code]; !exists {
			return code
		}
	}
}

// randomCode returns an n-char human-shareable code using an unambiguous alphabet
// (no I, L, O, 0, 1).
func randomCode(n int) string {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		for i := range b { // crypto/rand should not fail; jitter fallback
			b[i] = byte(time.Now().UnixNano() >> (i * 8))
		}
	}
	out := make([]byte, n)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}

func nowMs() int64 { return time.Now().UnixMilli() }
