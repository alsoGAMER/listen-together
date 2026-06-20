package room

import (
	"testing"

	"github.com/alsoGAMER/listen-together/internal/protocol"
)

func TestCreateAndJoin(t *testing.T) {
	m := New()
	r := m.Create("host1", "alice")
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
	if _, err := New().Join("NOPE12", "c1", "alice"); err == nil {
		t.Fatal("expected error joining unknown room")
	}
}

func TestTransportAuthority(t *testing.T) {
	m := New()
	r := m.Create("host1", "alice")
	_, _ = m.Join(r.ID(), "follower1", "bob")

	if ok := r.ApplyTransport("host1", protocol.TransportInput{Playing: true, PositionMs: 1000, TrackID: "t1", Queue: []string{"t1"}, QueueIndex: 0}); !ok {
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
	m := New()
	r := m.Create("host1", "alice")
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

func TestLeaveReassignsHostAndDeletesEmpty(t *testing.T) {
	m := New()
	r := m.Create("host1", "alice")
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
