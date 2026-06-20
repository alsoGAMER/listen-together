package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	in := Transport{Playing: true, PositionMs: 1500, TrackID: "t1", Queue: []string{"t1", "t2"}, QueueIndex: 0, ServerTimeMs: 123}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	env := Envelope{Event: EvRoomState, Data: data}
	wire, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}

	var back Envelope
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatal(err)
	}
	if back.Event != EvRoomState {
		t.Fatalf("event = %q", back.Event)
	}
	var out Transport
	if err := json.Unmarshal(back.Data, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip mismatch: %+v != %+v", out, in)
	}
}
