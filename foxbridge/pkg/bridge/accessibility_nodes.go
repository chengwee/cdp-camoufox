package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
)

var errAccessibilityChanged = errors.New("document changed during accessibility snapshot")

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
	dom, err := b.describeDOM(document, 0, -1, true)
	if err != nil {
		return nil, err
	}
	axNodes := flattenAXTree(snapshot.Tree)
	frameNodes := make(map[int]bool)
	var candidates []int
	var visit func(map[string]interface{})
	visit = func(node map[string]interface{}) {
		nodeType, _ := node["nodeType"].(float64)
		if nodeType == 1 || nodeType == 3 || nodeType == 9 {
			backendID := node["backendNodeId"].(int)
			candidates = append(candidates, backendID)
			if node["nodeName"] == "IFRAME" || node["nodeName"] == "FRAME" {
				frameNodes[backendID] = true
			}
		}
		for _, field := range []string{"children", "shadowRoots"} {
			children, _ := node[field].([]interface{})
			for _, child := range children {
				visit(child.(map[string]interface{}))
			}
		}
	}
	visit(dom)
	// Native trees can wait for pending accessibility updates. Pipeline a bounded
	// batch to overlap those waits without guessing any DOM identities.
	const batchSize = 32
	for start := 0; start < len(candidates); start += batchSize {
		end := start + batchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		matches := make([][]int, end-start)
		failures := make([]error, end-start)
		var pending sync.WaitGroup
		for index, backendID := range candidates[start:end] {
			pending.Add(1)
			go func(index, backendID int) {
				defer pending.Done()
				matches[index], failures[index] = b.accessibilityMatches(sessionID, document, backendID, expected)
			}(index, backendID)
		}
		pending.Wait()
		for index, failure := range failures {
			if failure != nil {
				if !errors.Is(failure, errAccessibilityChanged) {
					if currentVersion, err := b.domVersion(document); err == nil && currentVersion != version {
						return nil, errAccessibilityChanged
					}
				}
				return nil, failure
			}
			for _, axIndex := range matches[index] {
				axNodes[axIndex]["backendDOMNodeId"] = candidates[start+index]
			}
		}
	}
	currentVersion, err := b.domVersion(document)
	if err != nil {
		return nil, err
	}
	if currentVersion != version {
		return nil, errAccessibilityChanged
	}
	for _, node := range axNodes {
		if backendID, ok := node["backendDOMNodeId"].(int); ok && frameNodes[backendID] {
			delete(node, "children")
		}
	}
	return json.Marshal(snapshot)
}

func (b *Bridge) accessibilityMatches(sessionID string, document *domDocument, backendID int, expected []byte) ([]int, error) {
	object, err := b.domObject(sessionID, backendID)
	if err != nil {
		return nil, err
	}
	defer func() {
		b.callJuggler(sessionID, "Runtime.disposeObject", map[string]interface{}{"executionContextId": document.contextID, "objectId": object.ObjectID})
		b.nodeObjectsMu.Lock()
		delete(b.objectOwners, object.ObjectID)
		delete(b.objectContexts, object.ObjectID)
		b.nodeObjectsMu.Unlock()
	}()
	marked, err := b.callJuggler(sessionID, "Accessibility.getFullAXTree", map[string]interface{}{"objectId": object.ObjectID})
	if err != nil {
		return nil, err
	}
	var candidate struct {
		Tree map[string]interface{} `json:"tree"`
	}
	if err := json.Unmarshal(marked, &candidate); err != nil {
		return nil, err
	}
	signature, err := accessibilitySignature(candidate.Tree)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(signature, expected) {
		return nil, errAccessibilityChanged
	}
	var matches []int
	for index, node := range flattenAXTree(candidate.Tree) {
		if node["foundObject"] == true {
			matches = append(matches, index)
		}
	}
	return matches, nil
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
