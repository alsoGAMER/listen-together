// Package protocol defines the listen-together WebSocket wire format: the
// message envelope, event names, and every payload type exchanged between a
// client and the server.
//
// Every frame on the wire is a JSON object {"event": "<name>", "data": {...}}.
// This mirrors the {event, data} convention Feishin already uses in its
// remote-control feature, so a Feishin client and this server feel consistent.
//
// The package has no dependencies on the rest of the codebase so it can be
// vendored or copied verbatim into a client.
package protocol

import "encoding/json"

// Client -> Server event names.
const (
	EvAuthenticate   = "authenticate"
	EvCreateRoom     = "createRoom"
	EvJoinRoom       = "joinRoom"
	EvLeaveRoom      = "leaveRoom"
	EvRequestControl = "requestControl"
	EvPassControl    = "passControl"
	EvTransport      = "transport"
	EvPing           = "ping"
)

// Server -> Client event names.
const (
	EvAuthenticated    = "authenticated"
	EvRoomState        = "roomState"
	EvControlRequested = "controlRequested"
	EvPong             = "pong"
	EvError            = "error"
)

// Envelope is the generic on-the-wire frame. On the inbound path Data is left as
// raw JSON and decoded into the concrete payload once the event name is known.
type Envelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// ---- Client -> Server payloads ----

// AuthenticatePayload carries the user's own Subsonic credentials. They are used
// only to validate against that user's own server via a Subsonic ping; they are
// never shared with other room members.
type AuthenticatePayload struct {
	ServerURL string `json:"serverUrl"`
	Username  string `json:"username"`
	Token     string `json:"token"`    // Subsonic token = md5(password + salt)
	Salt      string `json:"salt"`     // Subsonic salt
	Password  string `json:"password"` // optional fallback if token/salt absent
}

// JoinRoomPayload identifies the room to join.
type JoinRoomPayload struct {
	RoomID string `json:"roomId"`
}

// PassControlPayload names the member that should become the new host.
type PassControlPayload struct {
	ToMemberID string `json:"toMemberId"`
}

// PingPayload carries the client's local send time for clock synchronization.
type PingPayload struct {
	T0 int64 `json:"t0"`
}

// ---- Shared types ----

// Transport is the authoritative playback state of a room. Only IDs and position
// are synced; stream URLs are never shared (each client builds its own from its
// own server session).
type Transport struct {
	Playing      bool     `json:"playing"`
	PositionMs   int64    `json:"positionMs"`
	TrackID      string   `json:"trackId"`
	Queue        []string `json:"queue"`
	QueueIndex   int      `json:"queueIndex"`
	ServerTimeMs int64    `json:"serverTimeMs"` // server-stamped; basis for clock sync
}

// TransportInput is what a host sends. ServerTimeMs is ignored on input and
// re-stamped by the server. ClientTimeMs is a monotonic logical clock stamped by
// the sending host; the server uses it to drop transports that arrive out of
// order, and ignores it (treats it as absent) when zero.
type TransportInput struct {
	Playing      bool     `json:"playing"`
	PositionMs   int64    `json:"positionMs"`
	TrackID      string   `json:"trackId"`
	Queue        []string `json:"queue"`
	QueueIndex   int      `json:"queueIndex"`
	ClientTimeMs int64    `json:"clientTimeMs"`
}

// Member is a participant in a room.
type Member struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// ---- Server -> Client payloads ----

// AuthenticatedPayload confirms a successful authentication and assigns the
// connection a stable member id.
type AuthenticatedPayload struct {
	MemberID string `json:"memberId"`
	Username string `json:"username"`
}

// RoomStatePayload is the full snapshot broadcast on any room change.
type RoomStatePayload struct {
	RoomID       string    `json:"roomId"`
	HostMemberID string    `json:"hostMemberId"`
	Members      []Member  `json:"members"`
	Seq          uint64    `json:"seq"`
	Transport    Transport `json:"transport"`
}

// ControlRequestedPayload notifies the host that a follower wants control.
type ControlRequestedPayload struct {
	FromMemberID string `json:"fromMemberId"`
	FromUsername string `json:"fromUsername"`
}

// PongPayload answers a ping, echoing T0 and stamping the server time.
type PongPayload struct {
	T0           int64 `json:"t0"`
	ServerTimeMs int64 `json:"serverTimeMs"`
}

// ErrorPayload is a human-readable error sent to a single client.
type ErrorPayload struct {
	Message string `json:"message"`
}
