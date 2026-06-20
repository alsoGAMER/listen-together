package hub_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/alsoGAMER/listen-together/internal/auth"
	"github.com/alsoGAMER/listen-together/internal/hub"
	"github.com/alsoGAMER/listen-together/internal/protocol"
)

// mockSubsonic answers /rest/ping.view with status ok.
func mockSubsonic(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/ping.view" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok","version":"1.16.1"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type wsConn struct {
	t    *testing.T
	conn *websocket.Conn
}

func (w *wsConn) sendRaw(event string, data interface{}) {
	w.t.Helper()
	raw, _ := json.Marshal(data)
	env, _ := json.Marshal(protocol.Envelope{Event: event, Data: raw})
	if err := w.conn.WriteMessage(websocket.TextMessage, env); err != nil {
		w.t.Fatalf("write %s: %v", event, err)
	}
}

func (w *wsConn) next() protocol.Envelope {
	w.t.Helper()
	_ = w.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := w.conn.ReadMessage()
	if err != nil {
		w.t.Fatalf("read: %v", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		w.t.Fatalf("unmarshal: %v", err)
	}
	return env
}

func (w *wsConn) expect(event string) protocol.Envelope {
	w.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if env := w.next(); env.Event == event {
			return env
		}
	}
	w.t.Fatalf("did not receive event %q in time", event)
	return protocol.Envelope{}
}

func dial(t *testing.T, srv *httptest.Server) *wsConn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &wsConn{t: t, conn: conn}
}

func authed(t *testing.T, srv *httptest.Server, subsonicURL, user string) (*wsConn, string) {
	t.Helper()
	c := dial(t, srv)
	c.sendRaw(protocol.EvAuthenticate, protocol.AuthenticatePayload{ServerURL: subsonicURL, Username: user, Token: "tok", Salt: "salt"})
	var p protocol.AuthenticatedPayload
	if err := json.Unmarshal(c.expect(protocol.EvAuthenticated).Data, &p); err != nil || p.MemberID == "" {
		t.Fatalf("bad authenticated payload: %v", err)
	}
	return c, p.MemberID
}

func roomState(t *testing.T, env protocol.Envelope) protocol.RoomStatePayload {
	t.Helper()
	var p protocol.RoomStatePayload
	if err := json.Unmarshal(env.Data, &p); err != nil {
		t.Fatalf("roomState unmarshal: %v", err)
	}
	return p
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	h := hub.New(auth.New(nil))
	srv := httptest.NewServer(http.HandlerFunc(h.ServeWS))
	t.Cleanup(srv.Close)
	return srv
}

func TestRequiresAuth(t *testing.T) {
	srv := newServer(t)
	c := dial(t, srv)
	c.sendRaw(protocol.EvCreateRoom, struct{}{})
	if env := c.expect(protocol.EvError); env.Event != protocol.EvError {
		t.Fatalf("expected error, got %q", env.Event)
	}
}

func TestEndToEndSyncFlow(t *testing.T) {
	subsonic := mockSubsonic(t)
	srv := newServer(t)

	host, hostID := authed(t, srv, subsonic.URL, "alice")
	host.sendRaw(protocol.EvCreateRoom, struct{}{})
	rs := roomState(t, host.expect(protocol.EvRoomState))
	if rs.HostMemberID != hostID {
		t.Fatalf("host = %q, want %q", rs.HostMemberID, hostID)
	}
	roomID := rs.RoomID
	if roomID == "" {
		t.Fatal("empty room id")
	}

	// Follower joins.
	follower, _ := authed(t, srv, subsonic.URL, "bob")
	follower.sendRaw(protocol.EvJoinRoom, protocol.JoinRoomPayload{RoomID: roomID})
	if got := len(roomState(t, follower.expect(protocol.EvRoomState)).Members); got != 2 {
		t.Fatalf("follower sees %d members, want 2", got)
	}
	host.expect(protocol.EvRoomState) // host gets the member-list update too

	// Host drives transport; both receive it.
	host.sendRaw(protocol.EvTransport, protocol.TransportInput{Playing: true, PositionMs: 1500, TrackID: "track-A", Queue: []string{"track-A"}, QueueIndex: 0})
	for _, c := range []*wsConn{host, follower} {
		ts := roomState(t, c.expect(protocol.EvRoomState)).Transport
		if !ts.Playing || ts.TrackID != "track-A" || ts.PositionMs != 1500 {
			t.Fatalf("transport not propagated: %+v", ts)
		}
		if ts.ServerTimeMs == 0 {
			t.Fatal("serverTimeMs not stamped on broadcast")
		}
	}

	// Follower's transport is ignored. Prove it: send transport then ping; the
	// next message must be the pong, not a roomState.
	follower.sendRaw(protocol.EvTransport, protocol.TransportInput{Playing: false, TrackID: "evil"})
	follower.sendRaw(protocol.EvPing, protocol.PingPayload{T0: 111})
	pong := follower.next()
	if pong.Event != protocol.EvPong {
		t.Fatalf("follower transport leaked a %q event; want pong", pong.Event)
	}
	var pp protocol.PongPayload
	_ = json.Unmarshal(pong.Data, &pp)
	if pp.T0 != 111 || pp.ServerTimeMs == 0 {
		t.Fatalf("bad pong: %+v", pp)
	}

	// Host disconnects -> follower becomes host.
	host.conn.Close()
	for {
		rs := roomState(t, follower.expect(protocol.EvRoomState))
		if len(rs.Members) == 1 {
			if got := rs.Members[0].Username; got != "bob" {
				t.Fatalf("remaining member = %q, want bob", got)
			}
			if rs.HostMemberID != rs.Members[0].ID {
				t.Fatalf("host not reassigned: host=%q member=%q", rs.HostMemberID, rs.Members[0].ID)
			}
			break
		}
	}
}

func TestPassControlFlow(t *testing.T) {
	subsonic := mockSubsonic(t)
	srv := newServer(t)

	host, _ := authed(t, srv, subsonic.URL, "alice")
	host.sendRaw(protocol.EvCreateRoom, struct{}{})
	roomID := roomState(t, host.expect(protocol.EvRoomState)).RoomID

	follower, followerID := authed(t, srv, subsonic.URL, "bob")
	follower.sendRaw(protocol.EvJoinRoom, protocol.JoinRoomPayload{RoomID: roomID})
	follower.expect(protocol.EvRoomState)
	host.expect(protocol.EvRoomState)

	host.sendRaw(protocol.EvPassControl, protocol.PassControlPayload{ToMemberID: followerID})
	rs := roomState(t, host.expect(protocol.EvRoomState))
	if rs.HostMemberID != followerID {
		t.Fatalf("host after pass = %q, want %q", rs.HostMemberID, followerID)
	}

	// Now the old host is a follower and cannot drive transport.
	host.sendRaw(protocol.EvTransport, protocol.TransportInput{TrackID: "nope"})
	host.sendRaw(protocol.EvPing, protocol.PingPayload{T0: 7})
	if env := host.next(); env.Event != protocol.EvPong {
		t.Fatalf("old host transport leaked %q; want pong", env.Event)
	}
}
