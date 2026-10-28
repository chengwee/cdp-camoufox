package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/VulpineOS/foxbridge/pkg/cdp"
)

func TestAccessibilityRejectsChangedCheckboxStates(t *testing.T) {
	b, backend := newTestBridge()
	b.latestCtx["session"] = "context"
	backend.SetResponse("session", "Runtime.evaluate", json.RawMessage(`{"result":{"type":"object","objectId":"registry"}}`), nil)
	for attempt := 0; attempt < 3; attempt++ {
		backend.QueueResponse("session", "Runtime.callFunction", json.RawMessage(`{"result":{"type":"number","value":0}}`), nil)
		backend.QueueResponse("session", "Runtime.callFunction", json.RawMessage(`{"result":{"value":{"localId":1,"nodeType":9,"nodeName":"#document","children":[]}}}`), nil)
		backend.QueueResponse("session", "Runtime.callFunction", json.RawMessage(`{"result":{"type":"object","subtype":"node","objectId":"document"}}`), nil)
		backend.QueueResponse("session", "Accessibility.getFullAXTree", json.RawMessage(`{"tree":{"role":"document","name":"Fixture","children":[{"role":"checkbutton","name":"Choice","checked":true},{"role":"checkbutton","name":"Choice","checked":false}]}}`), nil)
		backend.QueueResponse("session", "Accessibility.getFullAXTree", json.RawMessage(`{"tree":{"role":"document","name":"Fixture","foundObject":true,"children":[{"role":"checkbutton","name":"Choice","checked":false},{"role":"checkbutton","name":"Choice","checked":true}]}}`), nil)
	}
	_, cdpErr := b.handleAccessibility(nil, &cdp.Message{SessionID: "session", Method: "Accessibility.getFullAXTree", Params: json.RawMessage(`{}`)})
	if cdpErr == nil || !strings.Contains(cdpErr.Message, "document changed") {
		t.Fatalf("error = %v, want changed-document error", cdpErr)
	}
}

func TestAccessibilityRejectsConcurrentDOMMutation(t *testing.T) {
	b, backend := newTestBridge()
	b.latestCtx["session"] = "context"
	backend.SetResponse("session", "Runtime.evaluate", json.RawMessage(`{"result":{"type":"object","objectId":"registry"}}`), nil)
	for attempt := 0; attempt < 3; attempt++ {
		backend.QueueResponse("session", "Runtime.callFunction", json.RawMessage(`{"result":{"type":"number","value":0}}`), nil)
		backend.QueueResponse("session", "Runtime.callFunction", json.RawMessage(`{"result":{"value":{"localId":1,"nodeType":9,"nodeName":"#document","children":[]}}}`), nil)
		backend.QueueResponse("session", "Runtime.callFunction", json.RawMessage(`{"result":{"type":"object","subtype":"node","objectId":"document"}}`), nil)
		backend.QueueResponse("session", "Runtime.callFunction", json.RawMessage(`{"result":{"type":"number","value":1}}`), nil)
		backend.QueueResponse("session", "Accessibility.getFullAXTree", json.RawMessage(`{"tree":{"role":"document","name":"Fixture"}}`), nil)
		backend.QueueResponse("session", "Accessibility.getFullAXTree", json.RawMessage(`{"tree":{"role":"document","name":"Fixture","foundObject":true}}`), nil)
	}
	_, cdpErr := b.handleAccessibility(nil, &cdp.Message{SessionID: "session", Method: "Accessibility.getFullAXTree", Params: json.RawMessage(`{}`)})
	if cdpErr == nil || !strings.Contains(cdpErr.Message, "document changed") {
		t.Fatalf("error = %v, want changed-document error", cdpErr)
	}
}
