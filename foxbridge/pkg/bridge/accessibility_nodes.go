package bridge

import (
	"encoding/json"
	"fmt"
)

func (b *Bridge) associateNativeAccessibilityNodes(document *domDocument, result json.RawMessage) (json.RawMessage, error) {
	var snapshot struct {
		Tree     map[string]interface{} `json:"tree"`
		DOMNodes remoteObject           `json:"domNodes"`
	}
	if err := json.Unmarshal(result, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.DOMNodes.ObjectID == "" {
		return nil, fmt.Errorf("native accessibility requires a matching CDP Camoufox build: missing DOM identities")
	}
	defer b.callJuggler(document.sessionID, "Runtime.disposeObject", map[string]interface{}{"executionContextId": document.contextID, "objectId": snapshot.DOMNodes.ObjectID})
	ids, err := b.callRegistry(document, `function(registry, nodes) { return Array.from(nodes, node => registry.id(node)); }`, []map[string]interface{}{{"objectId": snapshot.DOMNodes.ObjectID}}, true)
	if err != nil {
		return nil, err
	}
	var localIDs []int
	if err := json.Unmarshal(ids.Value, &localIDs); err != nil {
		return nil, err
	}
	var visit func(map[string]interface{}) error
	visit = func(node map[string]interface{}) error {
		if node == nil {
			return nil
		}
		if index, ok := node["nodeIndex"].(float64); ok {
			if index < 0 || int(index) >= len(localIDs) || index != float64(int(index)) {
				return fmt.Errorf("invalid native DOM identity index")
			}
			identity := map[string]interface{}{"localId": float64(localIDs[int(index)])}
			b.registerDOMTree(document, identity)
			node["backendDOMNodeId"] = identity["backendNodeId"]
		}
		delete(node, "nodeIndex")
		delete(node, "backendNodeId")
		children, _ := node["children"].([]interface{})
		for _, child := range children {
			if err := visit(child.(map[string]interface{})); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(snapshot.Tree); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]interface{}{"tree": snapshot.Tree})
}
