package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chengwee/cdp-camoufox/foxbridge/pkg/cdp"
)

func TestNativeAccessibilityUsesOneTreeAndPreservesDOMIdentity(t *testing.T) {
	b, backend := newTestBridge()
	b.latestCtx["session"] = "context"
	backend.SetResponse("session", "Runtime.evaluate", json.RawMessage(`{"result":{"type":"object","objectId":"registry"}}`), nil)
	backend.SetResponse("session", "Runtime.callFunction", json.RawMessage(`{"result":{"value":[21,22,23]}}`), nil)
	backend.SetResponse("session", "Accessibility.getFullAXTree", json.RawMessage(`{"tree":{"role":"document","name":"Fixture","nodeIndex":0,"backendNodeId":1,"children":[{"role":"pushbutton","name":"Choice","nodeIndex":1,"backendNodeId":2},{"role":"pushbutton","name":"Choice","nodeIndex":2,"backendNodeId":3}]},"domNodes":{"type":"object","subtype":"array","objectId":"nodes"}}`), nil)
	document, err := b.domDocumentFor("session", "context")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]interface{}{"localId": float64(22)}
	b.registerDOMTree(document, known)
	result, cdpErr := b.handleAccessibility(nil, &cdp.Message{SessionID: "session", Method: "Accessibility.getFullAXTree"})
	if cdpErr != nil {
		t.Fatal(cdpErr)
	}
	var tree struct {
		Nodes []struct {
			ID int `json:"backendDOMNodeId"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(result, &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Nodes) != 3 || tree.Nodes[1].ID != known["backendNodeId"].(int) || tree.Nodes[2].ID == tree.Nodes[1].ID || tree.Nodes[2].ID == 0 {
		t.Fatalf("incorrect node identities: %s", result)
	}
	if calls := backend.CallsForMethod("Accessibility.getFullAXTree"); len(calls) != 1 {
		t.Fatalf("native tree requests = %d, want 1", len(calls))
	}
	if calls := backend.CallsForMethod("Runtime.disposeObject"); len(calls) != 1 || !strings.Contains(string(calls[0].Params), `"nodes"`) {
		t.Fatalf("native node batch not released: %v", calls)
	}
}

func TestNativeAccessibilityRejectsIncompatibleBrowser(t *testing.T) {
	b, backend := newTestBridge()
	b.latestCtx["session"] = "context"
	backend.SetResponse("session", "Runtime.evaluate", json.RawMessage(`{"result":{"type":"object","objectId":"registry"}}`), nil)
	backend.SetResponse("session", "Accessibility.getFullAXTree", json.RawMessage(`{"tree":{"role":"document","name":"Fixture"}}`), nil)
	_, err := b.handleAccessibility(nil, &cdp.Message{SessionID: "session", Method: "Accessibility.getFullAXTree"})
	if err == nil || !strings.Contains(err.Message, "matching CDP Camoufox build") {
		t.Fatalf("expected explicit incompatible-browser error, got %v", err)
	}
}
