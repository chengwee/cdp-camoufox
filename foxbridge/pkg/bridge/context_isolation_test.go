package bridge

import (
	"encoding/json"
	"testing"

	"github.com/chengwee/cdp-camoufox/foxbridge/pkg/cdp"
)

func TestContextLifecycleIsScopedToItsPage(t *testing.T) {
	b, backend := newTestBridge()
	b.SetupEventSubscriptions()
	for _, page := range []struct{ session, native, frame string }{{"first", "native-first", "frame-first"}, {"second", "native-second", "frame-second"}} {
		b.sessions.Add(&cdp.SessionInfo{SessionID: page.session, JugglerSessionID: page.native, FrameID: page.frame})
		params, _ := json.Marshal(map[string]interface{}{"executionContextId": "id-1", "auxData": map[string]string{"frameId": page.frame, "name": ""}})
		backend.handlers["Runtime.executionContextCreated"][0](page.native, params)
	}
	contextID, _ := b.contextForWorld("first", "frame-first", "")
	if contextID == 0 {
		t.Fatal("second page replaced first page's context metadata")
	}
	backend.handlers["Runtime.executionContextDestroyed"][0]("native-second", json.RawMessage(`{"executionContextId":"id-1"}`))
	params, _ := json.Marshal(map[string]interface{}{"contextId": contextID, "expression": "document.title", "returnByValue": true})
	backend.SetResponse("native-first", "Runtime.evaluate", json.RawMessage(`{"result":{"type":"string","value":"First"}}`), nil)
	result, err := b.handleRuntime(nil, &cdp.Message{SessionID: "first", Method: "Runtime.evaluate", Params: params})
	if err != nil || string(result) != `{"result":{"type":"string","value":"First"}}` {
		t.Fatalf("other page's navigation broke evaluation: %s, %v", result, err)
	}
	if id, _ := b.contextForWorld("first", "frame-first", ""); id != contextID {
		t.Fatal("first context metadata was removed with second page")
	}
	b.clearConnectionState([]*targetRecord{{pageSessionID: "first", jugglerSessionID: "native-first"}})
	if len(b.contextFrames) != 0 || len(b.contextNames) != 0 {
		t.Fatal("closed page retained context metadata")
	}
}

func TestBiDiContextDestroyedUsesCreatedUniqueID(t *testing.T) {
	b, backend, socketConn := lifecycleFixture(t)
	b.isBiDi = true
	backend.handlers["Runtime.executionContextCreated"][0]("juggler", json.RawMessage(`{"executionContextId":"realm-1","auxData":{"frameId":"main","name":""}}`))
	backend.handlers["Runtime.executionContextDestroyed"][0]("juggler", json.RawMessage(`{"executionContextId":"realm-1"}`))
	var created, destroyed string
	for _, event := range lifecycleEvents(t, b, socketConn) {
		if event.Method == "Runtime.executionContextCreated" {
			var payload struct {
				Context struct {
					UniqueID string `json:"uniqueId"`
				} `json:"context"`
			}
			json.Unmarshal(event.Params, &payload)
			created = payload.Context.UniqueID
		}
		if event.Method == "Runtime.executionContextDestroyed" {
			var payload struct {
				UniqueID string `json:"executionContextUniqueId"`
			}
			json.Unmarshal(event.Params, &payload)
			destroyed = payload.UniqueID
		}
	}
	if created == "" || destroyed != created {
		t.Fatalf("context unique IDs differ: created=%q destroyed=%q", created, destroyed)
	}
}
