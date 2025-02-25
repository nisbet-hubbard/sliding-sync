package testutils

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/matrix-org/gomatrixserverlib"
)

// Common functions between testing.T and testing.B
type TestBenchInterface interface {
	Fatalf(s string, args ...interface{})
	Errorf(s string, args ...interface{})
	Logf(s string, args ...interface{})
	Helper()
	Name() string
}

var (
	eventIDCounter = 0
	eventIDMu      sync.Mutex
)

type eventMock struct {
	Type           string      `json:"type"`
	StateKey       *string     `json:"state_key,omitempty"`
	Sender         string      `json:"sender"`
	Content        interface{} `json:"content"`
	EventID        string      `json:"event_id"`
	OriginServerTS int64       `json:"origin_server_ts"`
	Unsigned       interface{} `json:"unsigned,omitempty"`
}

func generateEventID(t TestBenchInterface) string {
	eventIDMu.Lock()
	defer eventIDMu.Unlock()
	eventIDCounter++
	// we need to mux in the test name bleurgh because when run using `go test ./...` each
	// package's tests run in parallel but with a shared database when run on Github Actions
	return fmt.Sprintf("$event_%d_%s", eventIDCounter, t.Name())
}

type eventMockModifier func(e *eventMock)

func WithTimestamp(ts time.Time) eventMockModifier {
	return func(e *eventMock) {
		e.OriginServerTS = int64(gomatrixserverlib.AsTimestamp(ts))
	}
}

func WithUnsigned(unsigned interface{}) eventMockModifier {
	return func(e *eventMock) {
		e.Unsigned = unsigned
	}
}

// Create a new m.room.member state event with membership: join for the given userID.
func NewJoinEvent(t TestBenchInterface, userID string, modifiers ...eventMockModifier) json.RawMessage {
	return NewStateEvent(t, "m.room.member", userID, userID, map[string]interface{}{
		"membership": "join",
	}, modifiers...)
}

func NewMessageEvent(t TestBenchInterface, userID, text string, modifiers ...eventMockModifier) json.RawMessage {
	return NewEvent(t, "m.room.message", userID, map[string]interface{}{
		"msgtype": "m.text",
		"body":    text,
	}, modifiers...)
}

func NewStateEvent(t TestBenchInterface, evType, stateKey, sender string, content interface{}, modifiers ...eventMockModifier) json.RawMessage {
	t.Helper()
	e := &eventMock{
		Type:           evType,
		StateKey:       &stateKey,
		Sender:         sender,
		Content:        content,
		EventID:        generateEventID(t),
		OriginServerTS: int64(gomatrixserverlib.AsTimestamp(time.Now())),
	}
	for _, m := range modifiers {
		m(e)
	}
	j, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("failed to make event JSON: %s", err)
	}
	return j
}

func NewEvent(t TestBenchInterface, evType, sender string, content interface{}, modifiers ...eventMockModifier) json.RawMessage {
	t.Helper()
	e := &eventMock{
		Type:           evType,
		Sender:         sender,
		Content:        content,
		EventID:        generateEventID(t),
		OriginServerTS: int64(gomatrixserverlib.AsTimestamp(time.Now())),
	}
	for _, m := range modifiers {
		m(e)
	}
	j, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("failed to make event JSON: %s", err)
	}
	return j
}

func NewAccountData(t *testing.T, evType string, content interface{}) json.RawMessage {
	e := struct {
		Type    string      `json:"type"`
		Content interface{} `json:"content"`
	}{
		Type:    evType,
		Content: content,
	}
	j, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("NewAccountData: failed to make event JSON: %s", err)
	}
	return j
}

func SetTimestamp(t *testing.T, event json.RawMessage, ts time.Time) json.RawMessage {
	parsed := eventMock{}
	err := json.Unmarshal(event, &parsed)
	if err != nil {
		t.Errorf("Failed to parse eventMock: %s", err)
		return nil
	}
	parsed.OriginServerTS = int64(gomatrixserverlib.AsTimestamp(ts))
	edited, err := json.Marshal(parsed)
	if err != nil {
		t.Errorf("Failed to serialise eventMock: %s", err)
		return nil
	}
	return edited
}
