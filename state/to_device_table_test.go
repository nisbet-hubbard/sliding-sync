package state

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/gomatrixserverlib"
)

func TestToDeviceTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	table := NewToDeviceTable(db)
	deviceID := "FOO"
	msgs := []gomatrixserverlib.SendToDeviceEvent{
		{
			Sender:  "alice",
			Type:    "something",
			Content: []byte(`{"foo":"bar"}`),
		},
		{
			Sender:  "bob",
			Type:    "something",
			Content: []byte(`{"foo":"bar2"}`),
		},
	}
	var lastPos int64
	if lastPos, err = table.InsertMessages(deviceID, msgs); err != nil {
		t.Fatalf("InsertMessages: %s", err)
	}
	if lastPos != 2 {
		t.Fatalf("InsertMessages: bad pos returned, got %d want 2", lastPos)
	}
	gotMsgs, err := table.Messages(deviceID, 0, lastPos)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if len(gotMsgs) != len(msgs) {
		t.Fatalf("Messages: got %d messages, want %d", len(gotMsgs), len(msgs))
	}
	for i := range msgs {
		want, err := json.Marshal(msgs[i])
		if err != nil {
			t.Fatalf("failed to marshal msg: %s", err)
		}
		if !bytes.Equal(want, gotMsgs[i]) {
			t.Fatalf("Messages: got %+v want %+v", gotMsgs[i], msgs[i])
		}
	}

	// same to= token, no messages
	gotMsgs, err = table.Messages(deviceID, lastPos, lastPos)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if len(gotMsgs) > 0 {
		t.Fatalf("Messages: got %d messages, want none", len(gotMsgs))
	}

	// different device ID, no messages
	gotMsgs, err = table.Messages("OTHER_DEVICE", 0, lastPos)
	if err != nil {
		t.Fatalf("Messages: %s", err)
	}
	if len(gotMsgs) > 0 {
		t.Fatalf("Messages: got %d messages, want none", len(gotMsgs))
	}
}