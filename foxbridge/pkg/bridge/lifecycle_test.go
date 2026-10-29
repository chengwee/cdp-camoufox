package bridge

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chengwee/cdp-camoufox/foxbridge/pkg/cdp"
	"github.com/gorilla/websocket"
)

func lifecycleFixture(t *testing.T) (*Bridge, *mockBackend, *websocket.Conn) {
	t.Helper()
	backend := newMockBackend()
	sessions := cdp.NewSessionManager()
	owners := make(chan *cdp.Connection, 1)
	server := cdp.NewServer(0, func(conn *cdp.Connection, _ *cdp.Message) { owners <- conn }, sessions)
	if err := os.MkdirAll("../../.tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("../../.tmp", "lifecycle-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	socket, err := filepath.Abs(filepath.Join(directory, "cdp.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server.SetUnixSocket(socket)
	b := New(backend, sessions, server)
	b.SetupEventSubscriptions()
	go server.Start()
	t.Cleanup(func() { server.Close() })
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	var socketConn *websocket.Conn
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		socketConn, _, err = dialer.Dial("ws://localhost/devtools/browser/foxbridge", nil)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socketConn.Close() })
	if err := socketConn.WriteJSON(&cdp.Message{ID: 1, Method: "fixture"}); err != nil {
		t.Fatal(err)
	}
	var owner *cdp.Connection
	select {
	case owner = <-owners:
	case <-time.After(time.Second):
		t.Fatal("connection not received")
	}
	sessions.Add(&cdp.SessionInfo{SessionID: "page-session", JugglerSessionID: "juggler", TargetID: "page", Type: "page", FrameID: "main", URL: "https://fixture.test"})
	b.ownership.registerPair(owner, &targetPair{pageTargetID: "page", pageSessionID: "page-session", jugglerSessionID: "juggler"})
	return b, backend, socketConn
}

func lifecycleEvents(t *testing.T, b *Bridge, socketConn *websocket.Conn) []cdp.Message {
	t.Helper()
	b.emitEvent("fixture.end", map[string]interface{}{}, "page-session")
	socketConn.SetReadDeadline(time.Now().Add(time.Second))
	var events []cdp.Message
	for {
		var event cdp.Message
		if err := socketConn.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Method == "fixture.end" {
			return events
		}
		events = append(events, event)
	}
}

func TestChildLoadDoesNotCompleteParentNavigation(t *testing.T) {
	b, backend, socketConn := lifecycleFixture(t)
	backend.handlers["Page.frameAttached"][0]("juggler", json.RawMessage(`{"frameId":"child","parentFrameId":"main"}`))
	backend.handlers["Page.navigationCommitted"][0]("juggler", json.RawMessage(`{"frameId":"main","navigationId":"parent-loader","url":"https://fixture.test"}`))
	backend.handlers["Page.navigationCommitted"][0]("juggler", json.RawMessage(`{"frameId":"child","navigationId":"child-loader","url":"https://fixture.test/child"}`))
	backend.handlers["Page.eventFired"][0]("juggler", json.RawMessage(`{"frameId":"child","name":"DOMContentLoaded"}`))
	backend.handlers["Page.eventFired"][0]("juggler", json.RawMessage(`{"frameId":"child","name":"load"}`))
	childEvents := lifecycleEvents(t, b, socketConn)
	for _, event := range childEvents {
		if event.Method == "Page.loadEventFired" || event.Method == "Page.domContentEventFired" {
			t.Fatalf("child completed parent: %s", event.Method)
		}
	}
	backend.handlers["Page.eventFired"][0]("juggler", json.RawMessage(`{"frameId":"main","name":"load"}`))
	parentEvents := lifecycleEvents(t, b, socketConn)
	found := false
	for _, event := range parentEvents {
		if event.Method != "Page.lifecycleEvent" {
			continue
		}
		var lifecycle struct {
			LoaderID string `json:"loaderId"`
			Name     string `json:"name"`
		}
		json.Unmarshal(event.Params, &lifecycle)
		if lifecycle.Name == "load" {
			found = true
			if lifecycle.LoaderID != "parent-loader" {
				t.Fatalf("parent got child loader: %s", event.Params)
			}
		}
	}
	if !found {
		t.Fatal("parent load missing")
	}
}

func TestNativeReloadWaitsForRealLifecycleEvents(t *testing.T) {
	b, _, socketConn := lifecycleFixture(t)
	if _, err := b.handlePage(nil, &cdp.Message{SessionID: "page-session", Method: "Page.reload"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	for _, event := range lifecycleEvents(t, b, socketConn) {
		if event.Method == "Page.loadEventFired" || event.Method == "Page.domContentEventFired" || event.Method == "Page.frameNavigated" {
			t.Fatalf("reload fabricated completion: %s", event.Method)
		}
	}
}
