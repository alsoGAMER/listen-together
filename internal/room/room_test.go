package room

import (
	"testing"

	"github.com/alsoGAMER/listen-together/internal/protocol"
)

// sl returns a pointer to a string slice, for setting TransportInput.Queue.
func sl(ids ...string) *[]string {
	s := ids
	return &s
}

func TestCreateAndJoin(t *testing.T) {
	m := New(0, 0)
	r, _ := m.Create("host1", "alice")
	if r.ID() == "" {
		t.Fatal("expected non-empty room code")
	}
	if r.HostID() != "host1" {
		t.Fatalf("host = %q, want host1", r.HostID())
	}
	if len(r.Snapshot().Members) != 1 {
		t.Fatalf("members = %d, want 1", len(r.Snapshot().Members))
	}

	if _, err := m.Join(r.ID(), "follower1", "bob"); err != nil {
		t.Fatalf("join: %v", err)
	}
	if len(r.Snapshot().Members) != 2 {
		t.Fatalf("members after join = %d, want 2", len(r.Snapshot().Members))
	}
}

func TestJoinUnknownRoom(t *testing.T) {
	if _, err := New(0, 0).Join("NOPE12", "c1", "alice"); err == nil {
		t.Fatal("expected error joining unknown room")
	}
}

func TestTransportAuthority(t *testing.T) {
	m := New(0, 0)
	r, _ := m.Create("host1", "alice")
	_, _ = m.Join(r.ID(), "follower1", "bob")

	if ok := r.ApplyTransport("host1", protocol.TransportInput{Playing: true, PositionMs: 1000, TrackID: "t1", Queue: sl("t1"), QueueIndex: 0}); !ok {
		t.Fatal("host ApplyTransport returned false")
	}
	snap := r.Snapshot()
	if !snap.Transport.Playing || snap.Transport.TrackID != "t1" {
		t.Fatalf("transport not applied: %+v", snap.Transport)
	}
	if snap.Transport.ServerTimeMs == 0 {
		t.Fatal("serverTimeMs not stamped")
	}
	if snap.Seq != 1 {
		t.Fatalf("seq = %d, want 1", snap.Seq)
	}

	if ok := r.ApplyTransport("follower1", protocol.TransportInput{TrackID: "bad"}); ok {
		t.Fatal("follower ApplyTransport returned true; want false")
	}
	if got := r.Snapshot().Transport.TrackID; got != "t1" {
		t.Fatalf("follower mutated transport: trackId = %q", got)
	}
}

func TestPassControl(t *testing.T) {
	m := New(0, 0)
	r, _ := m.Create("host1", "alice")
	_, _ = m.Join(r.ID(), "follower1", "bob")

	if ok := r.PassControl("follower1", "host1"); ok {
		t.Fatal("non-host passed control")
	}
	if ok := r.PassControl("host1", "follower1"); !ok {
		t.Fatal("host failed to pass control")
	}
	if got := r.HostID(); got != "follower1" {
		t.Fatalf("host after pass = %q, want follower1", got)
	}
	if ok := r.PassControl("follower1", "ghost"); ok {
		t.Fatal("passed control to non-member")
	}
}

func TestTransportMonotonicity(t *testing.T) {
	m := New(0, 0)
	r, _ := m.Create("host1", "alice")
	_, _ = m.Join(r.ID(), "follower1", "bob")

	if ok := r.ApplyTransport("host1", protocol.TransportInput{TrackID: "t1", ClientTimeMs: 100}); !ok {
		t.Fatal("first transport rejected")
	}
	// A stale (lower clock) transport from the same host must be dropped.
	if ok := r.ApplyTransport("host1", protocol.TransportInput{TrackID: "stale", ClientTimeMs: 50}); ok {
		t.Fatal("stale transport accepted; want dropped")
	}
	if got := r.Snapshot().Transport.TrackID; got != "t1" {
		t.Fatalf("stale transport mutated state: trackId = %q", got)
	}
	// A newer clock is accepted.
	if ok := r.ApplyTransport("host1", protocol.TransportInput{TrackID: "t2", ClientTimeMs: 150}); !ok {
		t.Fatal("newer transport rejected")
	}

	// Passing control resets the logical clock so the new host's unrelated clock
	// is not treated as stale.
	if ok := r.PassControl("host1", "follower1"); !ok {
		t.Fatal("pass control failed")
	}
	if ok := r.ApplyTransport("follower1", protocol.TransportInput{TrackID: "t3", ClientTimeMs: 1}); !ok {
		t.Fatal("new host's low clock rejected after pass-control; clock not reset")
	}
	if got := r.Snapshot().Transport.TrackID; got != "t3" {
		t.Fatalf("new host transport not applied: trackId = %q", got)
	}
}

// A zero ClientTimeMs means the client doesn't stamp a clock, so the guard is
// skipped and every transport is accepted in order.
func TestTransportNoClockAlwaysApplies(t *testing.T) {
	m := New(0, 0)
	r, _ := m.Create("host1", "alice")
	for i, track := range []string{"a", "b", "c"} {
		if ok := r.ApplyTransport("host1", protocol.TransportInput{TrackID: track}); !ok {
			t.Fatalf("transport %d (%s) rejected with zero clock", i, track)
		}
	}
	if got := r.Snapshot().Transport.TrackID; got != "c" {
		t.Fatalf("final trackId = %q, want c", got)
	}
}

func TestTransportQueueDiff(t *testing.T) {
	m := New(0, 0)
	r, _ := m.Create("host1", "alice")

	// Initial transport sets a queue.
	r.ApplyTransport("host1", protocol.TransportInput{TrackID: "a", Queue: sl("a", "b", "c"), QueueIndex: 0})
	if got := r.Snapshot().Transport.Queue; len(got) != 3 {
		t.Fatalf("initial queue = %v, want 3 items", got)
	}

	// A nil queue (omitted) keeps the existing one while still updating position.
	r.ApplyTransport("host1", protocol.TransportInput{TrackID: "b", Queue: nil, QueueIndex: 1, PositionMs: 5000})
	snap := r.Snapshot().Transport
	if len(snap.Queue) != 3 || snap.Queue[1] != "b" {
		t.Fatalf("omitted queue not preserved: %v", snap.Queue)
	}
	if snap.QueueIndex != 1 || snap.PositionMs != 5000 {
		t.Fatalf("non-queue fields not updated: idx=%d pos=%d", snap.QueueIndex, snap.PositionMs)
	}

	// A non-nil queue replaces it.
	r.ApplyTransport("host1", protocol.TransportInput{TrackID: "x", Queue: sl("x", "y"), QueueIndex: 0})
	if got := r.Snapshot().Transport.Queue; len(got) != 2 || got[0] != "x" {
		t.Fatalf("queue not replaced: %v", got)
	}

	// An explicit empty queue clears it.
	r.ApplyTransport("host1", protocol.TransportInput{TrackID: "", Queue: sl(), QueueIndex: -1})
	if got := r.Snapshot().Transport.Queue; len(got) != 0 {
		t.Fatalf("queue not cleared: %v", got)
	}
}

func TestMaxRooms(t *testing.T) {
	m := New(2, 0)
	if _, err := m.Create("h1", "a"); err != nil {
		t.Fatalf("create 1: %v", err)
	}
	if _, err := m.Create("h2", "b"); err != nil {
		t.Fatalf("create 2: %v", err)
	}
	if _, err := m.Create("h3", "c"); err == nil {
		t.Fatal("create 3 should fail at room cap")
	}
}

func TestMaxMembersPerRoom(t *testing.T) {
	m := New(0, 2)
	r, err := m.Create("h1", "a") // host counts as a member
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := m.Join(r.ID(), "f1", "b"); err != nil {
		t.Fatalf("join at cap edge: %v", err)
	}
	if _, err := m.Join(r.ID(), "f2", "c"); err == nil {
		t.Fatal("join past member cap should fail")
	}
	// A member already in the room may "rejoin" (reconnect) without tripping the cap.
	if _, err := m.Join(r.ID(), "f1", "b"); err != nil {
		t.Fatalf("idempotent rejoin rejected: %v", err)
	}
}

func TestCounts(t *testing.T) {
	m := New(0, 0)
	if rooms, members := m.Counts(); rooms != 0 || members != 0 {
		t.Fatalf("empty counts = (%d,%d), want (0,0)", rooms, members)
	}
	r, _ := m.Create("h1", "a")
	_, _ = m.Join(r.ID(), "f1", "b")
	_, _ = m.Create("h2", "c")
	if rooms, members := m.Counts(); rooms != 2 || members != 3 {
		t.Fatalf("counts = (%d,%d), want (2,3)", rooms, members)
	}
}

func TestLeaveReassignsHostAndDeletesEmpty(t *testing.T) {
	m := New(0, 0)
	r, _ := m.Create("host1", "alice")
	_, _ = m.Join(r.ID(), "follower1", "bob")

	room, deleted, hostChanged := m.Leave(r.ID(), "host1")
	if room == nil || deleted || !hostChanged {
		t.Fatalf("unexpected leave result: deleted=%v hostChanged=%v", deleted, hostChanged)
	}
	if got := r.HostID(); got != "follower1" {
		t.Fatalf("host after reassign = %q, want follower1", got)
	}

	_, deleted, _ = m.Leave(r.ID(), "follower1")
	if !deleted {
		t.Fatal("expected room deletion when last member leaves")
	}
	if _, ok := m.Get(r.ID()); ok {
		t.Fatal("room still present after deletion")
	}
}
