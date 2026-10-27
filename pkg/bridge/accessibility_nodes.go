package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Juggler marks one supplied DOM handle with foundObject. Never infer identity
// from role/name: repeated labels can represent different actionable elements.
func (b *Bridge) associateAccessibilityNodes(sessionID string, document *domDocument, version int, result json.RawMessage) (json.RawMessage, error) {
	var snapshot struct {
		Tree map[string]interface{} `json:"tree"`
	}
	if err := json.Unmarshal(result, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Tree == nil {
		return result, nil
	}
	expected, err := accessibilitySignature(snapshot.Tree)
	if err != nil {
		return nil, err
	}
	dom, err := b.describeDOM(document, 0, -1)
	if err != nil {
		return nil, err
	}
	axNodes := flattenAXTree(snapshot.Tree)
	frameNodes := make(map[int]bool)
	var visit func(map[string]interface{}) error
	visit = func(node map[string]interface{}) error {
		nodeType, _ := node["nodeType"].(float64)
		if nodeType == 1 || nodeType == 3 || nodeType == 9 {
			backendID := node["backendNodeId"].(int)
			if node["nodeName"] == "IFRAME" || node["nodeName"] == "FRAME" {
				frameNodes[backendID] = true
			}
			object, err := b.domObject(sessionID, backendID)
			if err != nil {
				return err
			}
			marked, callErr := b.callJuggler(sessionID, "Accessibility.getFullAXTree", map[string]interface{}{"objectId": object.ObjectID})
			b.callJuggler(sessionID, "Runtime.disposeObject", map[string]interface{}{"executionContextId": document.contextID, "objectId": object.ObjectID})
			if callErr != nil {
				return callErr
			}
			var candidate struct {
				Tree map[string]interface{} `json:"tree"`
			}
			if err := json.Unmarshal(marked, &candidate); err != nil {
				return err
			}
			candidateNodes := flattenAXTree(candidate.Tree)
			signature, err := accessibilitySignature(candidate.Tree)
			if err != nil {
				return err
			}
			if !bytes.Equal(signature, expected) || len(candidateNodes) != len(axNodes) {
				return fmt.Errorf("document changed during accessibility snapshot")
			}
			for index, candidateNode := range candidateNodes {
				if candidateNode["foundObject"] == true {
					axNodes[index]["backendDOMNodeId"] = backendID
				}
			}
		}
		for _, field := range []string{"children", "shadowRoots"} {
			children, _ := node[field].([]interface{})
			for _, child := range children {
				if err := visit(child.(map[string]interface{})); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(dom); err != nil {
		return nil, err
	}
	currentVersion, err := b.domVersion(document)
	if err != nil {
		return nil, err
	}
	if currentVersion != version {
		return nil, fmt.Errorf("document changed during accessibility snapshot")
	}
	for _, node := range axNodes {
		if backendID, ok := node["backendDOMNodeId"].(int); ok && frameNodes[backendID] {
			delete(node, "children")
		}
	}
	return json.Marshal(snapshot)
}

func accessibilitySignature(root map[string]interface{}) ([]byte, error) {
	var clean func(map[string]interface{}) map[string]interface{}
	clean = func(node map[string]interface{}) map[string]interface{} {
		copy := make(map[string]interface{}, len(node))
		for name, value := range node {
			if name == "foundObject" {
				continue
			}
			if name == "children" {
				children, _ := value.([]interface{})
				cleaned := make([]interface{}, 0, len(children))
				for _, child := range children {
					cleaned = append(cleaned, clean(child.(map[string]interface{})))
				}
				copy[name] = cleaned
			} else {
				copy[name] = value
			}
		}
		return copy
	}
	return json.Marshal(clean(root))
}

func flattenAXTree(root map[string]interface{}) []map[string]interface{} {
	if root == nil {
		return nil
	}
	nodes := []map[string]interface{}{root}
	children, _ := root["children"].([]interface{})
	for _, child := range children {
		nodes = append(nodes, flattenAXTree(child.(map[string]interface{}))...)
	}
	return nodes
}
