package bridge

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/VulpineOS/foxbridge/pkg/cdp"
)

func TestHandleDOM_EnableDisable(t *testing.T) {
	for _, method := range []string{"DOM.enable", "DOM.disable"} {
		t.Run(method, func(t *testing.T) {
			b, _ := newTestBridge()
			result, cdpErr := b.handleDOM(nil, &cdp.Message{ID: 1, Method: method, Params: json.RawMessage(`{}`)})
			if cdpErr != nil || string(result) != "{}" {
				t.Fatalf("result = %s, error = %v", result, cdpErr)
			}
		})
	}
}

func TestHandleDOM_UnknownMethod(t *testing.T) {
	b, _ := newTestBridge()
	_, cdpErr := b.handleDOM(nil, &cdp.Message{Method: "DOM.doesNotExist", Params: json.RawMessage(`{}`)})
	if cdpErr == nil || cdpErr.Code != -32601 {
		t.Fatalf("error = %v, want -32601", cdpErr)
	}
}

func TestHandleDOM_UnknownNode(t *testing.T) {
	methods := []string{
		"DOM.resolveNode", "DOM.describeNode", "DOM.querySelector", "DOM.querySelectorAll",
		"DOM.getBoxModel", "DOM.getContentQuads", "DOM.focus", "DOM.scrollIntoViewIfNeeded",
		"DOM.getAttributes", "DOM.getOuterHTML", "DOM.removeNode", "DOM.setAttributeValue",
		"DOM.setNodeValue", "DOM.setOuterHTML", "DOM.setFileInputFiles",
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			b, _ := newTestBridge()
			_, cdpErr := b.handleDOM(nil, &cdp.Message{Method: method, Params: json.RawMessage(`{"nodeId":999,"selector":"div","files":["/example.txt"]}`)})
			if cdpErr == nil || cdpErr.Code != -32000 {
				t.Fatalf("error = %v, want -32000", cdpErr)
			}
		})
	}
}

func TestHandleDOM_GetDocumentEvaluationFailure(t *testing.T) {
	b, backend := newTestBridge()
	b.latestCtx["session"] = "context"
	backend.SetResponse("session", "Runtime.evaluate", nil, fmt.Errorf("evaluation failed"))
	_, cdpErr := b.handleDOM(nil, &cdp.Message{SessionID: "session", Method: "DOM.getDocument", Params: json.RawMessage(`{}`)})
	if cdpErr == nil || cdpErr.Code != -32000 || !strings.Contains(cdpErr.Message, "evaluation failed") {
		t.Fatalf("error = %v, want evaluation failure", cdpErr)
	}
}

func TestHandleDOM_DocumentIdentityAndOwnership(t *testing.T) {
	b, backend := newTestBridge()
	b.latestCtx["session"] = "context"
	backend.SetResponse("session", "Runtime.evaluate", json.RawMessage(`{"result":{"type":"object","objectId":"registry"}}`), nil)
	backend.SetResponse("session", "Runtime.callFunction", json.RawMessage(`{"result":{"type":"object","value":{"localId":1,"nodeType":9,"nodeName":"#document","documentURL":"https://example.test/","children":[{"localId":2,"nodeType":1,"nodeName":"HTML","attributes":["lang","en"],"children":[]}]}}}`), nil)
	getDocument := &cdp.Message{SessionID: "session", Method: "DOM.getDocument", Params: json.RawMessage(`{}`)}
	type node struct {
		NodeID        int      `json:"nodeId"`
		BackendNodeID int      `json:"backendNodeId"`
		NodeType      int      `json:"nodeType"`
		NodeName      string   `json:"nodeName"`
		DocumentURL   string   `json:"documentURL"`
		Attributes    []string `json:"attributes"`
		Children      []struct {
			NodeID        int               `json:"nodeId"`
			BackendNodeID int               `json:"backendNodeId"`
			NodeType      int               `json:"nodeType"`
			NodeName      string            `json:"nodeName"`
			Attributes    []string          `json:"attributes"`
			Children      []json.RawMessage `json:"children"`
		} `json:"children"`
	}
	readDocument := func() node {
		t.Helper()
		result, cdpErr := b.handleDOM(nil, getDocument)
		if cdpErr != nil {
			t.Fatalf("getDocument: %v", cdpErr)
		}
		var response struct {
			Root node `json:"root"`
		}
		if err := json.Unmarshal(result, &response); err != nil {
			t.Fatal(err)
		}
		return response.Root
	}
	root := readDocument()
	if root.NodeID == 0 || root.NodeID != root.BackendNodeID || root.NodeType != 9 || root.NodeName != "#document" || root.DocumentURL != "https://example.test/" || len(root.Children) != 1 {
		t.Fatalf("unexpected document: %+v", root)
	}
	child := root.Children[0]
	if child.NodeID == 0 || child.NodeID == root.NodeID || child.NodeID != child.BackendNodeID || child.NodeType != 1 || child.NodeName != "HTML" || !reflect.DeepEqual(child.Attributes, []string{"lang", "en"}) || len(child.Children) != 0 {
		t.Fatalf("unexpected child: %+v", child)
	}
	again := readDocument()
	if again.NodeID != root.NodeID || len(again.Children) != 1 || again.Children[0].NodeID != child.NodeID {
		t.Fatalf("IDs changed: %+v", again)
	}
	backend.SetResponse("session", "Runtime.callFunction", json.RawMessage(`{"result":{"type":"object","subtype":"node","objectId":"node-object"}}`), nil)
	params := json.RawMessage(fmt.Sprintf(`{"nodeId":%d}`, child.NodeID))
	result, cdpErr := b.handleDOM(nil, &cdp.Message{SessionID: "session", Method: "DOM.resolveNode", Params: params})
	if cdpErr != nil {
		t.Fatalf("resolveNode: %v", cdpErr)
	}
	var resolved struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := json.Unmarshal(result, &resolved); err != nil {
		t.Fatal(err)
	}
	if resolved.Object.ObjectID != "node-object" {
		t.Fatalf("resolved object = %+v", resolved.Object)
	}
	for _, method := range []string{"DOM.resolveNode", "DOM.describeNode", "DOM.querySelector", "DOM.focus", "DOM.setFileInputFiles"} {
		t.Run("foreign node/"+method, func(t *testing.T) {
			_, cdpErr := b.handleDOM(nil, &cdp.Message{SessionID: "other", Method: method, Params: params})
			if cdpErr == nil || cdpErr.Code != -32000 {
				t.Fatalf("error = %v, want -32000", cdpErr)
			}
		})
	}
	_, cdpErr = b.handleDOM(nil, &cdp.Message{SessionID: "other", Method: "DOM.focus", Params: json.RawMessage(`{"objectId":"node-object"}`)})
	if cdpErr == nil || cdpErr.Code != -32000 {
		t.Fatalf("foreign object error = %v, want -32000", cdpErr)
	}
}
