package bridge

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/VulpineOS/foxbridge/pkg/cdp"
)

type accessibilityBackend struct {
	*mockBackend
	mu                                  sync.Mutex
	active, maximum, captures, disposed int
	changeOnce                          bool
	delayProbes                         bool
	detachOnce                          bool
	revision                            int
}

func (backend *accessibilityBackend) Call(session, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "Runtime.evaluate":
		return json.RawMessage(`{"result":{"type":"object","objectId":"registry"}}`), nil
	case "Runtime.callFunction":
		var call struct {
			Function string `json:"functionDeclaration"`
			Args     []struct {
				Value int `json:"value"`
			} `json:"args"`
		}
		json.Unmarshal(params, &call)
		switch call.Function {
		case `function(registry) { return registry.version(); }`:
			backend.mu.Lock()
			revision := backend.revision
			backend.mu.Unlock()
			return json.RawMessage(fmt.Sprintf(`{"result":{"type":"number","value":%d}}`, revision)), nil
		case `function(registry, localId) { return registry.node(localId); }`:
			backend.mu.Lock()
			shouldDetach := backend.detachOnce && backend.captures == 1 && call.Args[1].Value == 2
			if shouldDetach {
				backend.revision++
			}
			backend.mu.Unlock()
			if shouldDetach {
				return json.RawMessage(`{"exceptionDetails":{"text":"Node is detached"}}`), nil
			}
			return json.RawMessage(fmt.Sprintf(`{"result":{"type":"object","subtype":"node","objectId":"node-%d"}}`, call.Args[1].Value)), nil
		default:
			return json.RawMessage(`{"result":{"value":{"localId":1,"nodeType":9,"nodeName":"#document","children":[{"localId":2,"nodeType":1,"nodeName":"BUTTON","children":[]},{"localId":3,"nodeType":1,"nodeName":"BUTTON","children":[]}]}}}`), nil
		}
	case "Runtime.disposeObject":
		backend.mu.Lock()
		backend.disposed++
		backend.mu.Unlock()
		return json.RawMessage(`{}`), nil
	case "Accessibility.getFullAXTree":
		var call struct {
			ObjectID string `json:"objectId"`
		}
		json.Unmarshal(params, &call)
		backend.mu.Lock()
		if call.ObjectID == "" {
			backend.captures++
		}
		capture := backend.captures
		backend.active++
		if backend.active > backend.maximum {
			backend.maximum = backend.active
		}
		backend.mu.Unlock()
		if call.ObjectID != "" && backend.delayProbes {
			time.Sleep(20 * time.Millisecond)
		}
		backend.mu.Lock()
		backend.active--
		backend.mu.Unlock()
		name := "Fixture"
		if backend.changeOnce && capture == 1 && call.ObjectID != "" {
			name = "Changed"
		}
		tree := map[string]interface{}{"role": "document", "name": name, "children": []interface{}{
			map[string]interface{}{"role": "pushbutton", "name": "Choice"}, map[string]interface{}{"role": "pushbutton", "name": "Choice"},
		}}
		if call.ObjectID == "node-1" {
			tree["foundObject"] = true
		}
		if call.ObjectID == "node-2" {
			tree["children"].([]interface{})[0].(map[string]interface{})["foundObject"] = true
		}
		if call.ObjectID == "node-3" {
			tree["children"].([]interface{})[1].(map[string]interface{})["foundObject"] = true
		}
		return json.Marshal(map[string]interface{}{"tree": tree})
	}
	return backend.mockBackend.Call(session, method, params)
}

func TestAccessibilityPipelinesProbesWithoutMixingDuplicateLabels(t *testing.T) {
	backend := &accessibilityBackend{mockBackend: newMockBackend(), delayProbes: true}
	b := New(backend, cdp.NewSessionManager(), nil)
	b.latestCtx["session"] = "context"
	result, err := b.handleAccessibility(nil, &cdp.Message{SessionID: "session", Method: "Accessibility.getFullAXTree"})
	if err != nil {
		t.Fatal(err)
	}
	var tree struct {
		Nodes []struct {
			BackendID int `json:"backendDOMNodeId"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(result, &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Nodes) != 3 || tree.Nodes[1].BackendID == 0 || tree.Nodes[2].BackendID == 0 || tree.Nodes[1].BackendID == tree.Nodes[2].BackendID {
		t.Fatalf("duplicate-label identities lost: %s", result)
	}
	if backend.maximum < 2 {
		t.Fatal("native probes did not overlap")
	}
	if backend.disposed != 3 {
		t.Fatalf("disposed %d handles, want 3", backend.disposed)
	}
}

func TestAccessibilityRetriesTransientStateChange(t *testing.T) {
	backend := &accessibilityBackend{mockBackend: newMockBackend(), changeOnce: true}
	b := New(backend, cdp.NewSessionManager(), nil)
	b.latestCtx["session"] = "context"
	result, err := b.handleAccessibility(nil, &cdp.Message{SessionID: "session", Method: "Accessibility.getFullAXTree"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.captures < 2 {
		t.Fatal("transient capture was not retried")
	}
	var tree struct {
		Nodes []struct {
			Name struct {
				Value string `json:"value"`
			} `json:"name"`
		} `json:"nodes"`
	}
	json.Unmarshal(result, &tree)
	if len(tree.Nodes) != 3 || tree.Nodes[0].Name.Value != "Fixture" {
		t.Fatalf("mixed snapshot: %s", result)
	}
}

func TestAccessibilityRetriesDetachedCandidateAfterDOMChange(t *testing.T) {
	backend := &accessibilityBackend{mockBackend: newMockBackend(), detachOnce: true}
	b := New(backend, cdp.NewSessionManager(), nil)
	b.latestCtx["session"] = "context"
	_, err := b.handleAccessibility(nil, &cdp.Message{SessionID: "session", Method: "Accessibility.getFullAXTree"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.captures < 2 {
		t.Fatal("capture did not retry after DOM removal")
	}
}
