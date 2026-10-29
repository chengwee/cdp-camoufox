package bridge

import (
	"encoding/json"
	"fmt"
)

type domDocument struct {
	sessionID string
	contextID string
	objectID  string
	localIDs  map[int]int
}

type domNodeRef struct {
	document *domDocument
	localID  int
}

type remoteObject struct {
	Type     string          `json:"type"`
	Subtype  string          `json:"subtype,omitempty"`
	ObjectID string          `json:"objectId,omitempty"`
	Value    json.RawMessage `json:"value,omitempty"`
}

// The registry is a remote object, not a property installed on the page's globals.
const domRegistryExpression = `(() => {
	const ids = new WeakMap();
	const nodes = new Map();
	let next = 0;
	const id = node => {
		if (!node || !node.nodeType) throw new Error('Not a DOM node');
		if (!ids.has(node)) { ids.set(node, ++next); nodes.set(next, node); }
		return ids.get(node);
	};
	const node = key => {
		const value = nodes.get(key);
		if (!value || (value.nodeType !== 9 && !value.isConnected)) throw new Error('Node is detached');
		return value;
	};
	const describe = (value, depth) => {
		const result = {
			localId: id(value), nodeType: value.nodeType, nodeName: value.nodeName,
			localName: value.localName || '', nodeValue: value.nodeValue || '',
			childNodeCount: value.childNodes.length,
			attributes: value.attributes ? Array.from(value.attributes).flatMap(attribute => [attribute.name, attribute.value]) : []
		};
		if (value.nodeType === 9) {
			result.documentURL = value.URL;
			result.baseURL = value.baseURI;
		}
		if (depth !== 0) {
			result.children = Array.from(value.childNodes, child => describe(child, depth - 1));
			if (value.shadowRoot) result.shadowRoots = [describe(value.shadowRoot, depth - 1)];
		}
		if (value.nodeType === 11 && value.host) result.shadowRootType = 'open';
		return result;
	};
	return {id, node, describe, document};
})()`

func decodeRemote(result json.RawMessage) (remoteObject, error) {
	var response struct {
		Result           remoteObject    `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return remoteObject{}, err
	}
	if len(response.ExceptionDetails) != 0 && string(response.ExceptionDetails) != "null" {
		return remoteObject{}, fmt.Errorf("browser evaluation failed: %s", response.ExceptionDetails)
	}
	return response.Result, nil
}

func (b *Bridge) rememberRuntimeObject(sessionID, contextID string, result json.RawMessage) {
	object, err := decodeRemote(result)
	if err != nil || object.ObjectID == "" {
		return
	}
	b.nodeObjectsMu.Lock()
	b.objectOwners[object.ObjectID] = sessionID
	b.objectContexts[object.ObjectID] = contextID
	b.nodeObjectsMu.Unlock()
}

func (b *Bridge) domDocumentFor(sessionID, contextID string) (*domDocument, error) {
	if contextID == "" {
		contextID = b.latestContextForSession(sessionID)
	}
	if contextID == "" {
		return nil, fmt.Errorf("execution context not available")
	}
	if frameID := b.frameIDForContext(sessionID, contextID); frameID != "" {
		if _, mainContext := b.contextForWorld(sessionID, frameID, ""); mainContext != "" {
			contextID = mainContext
		}
	}
	key := sessionID + "\x00" + contextID
	b.domMu.Lock()
	defer b.domMu.Unlock()
	if document := b.domDocuments[key]; document != nil {
		return document, nil
	}
	result, err := b.callJuggler(sessionID, "Runtime.evaluate", map[string]interface{}{
		"executionContextId": contextID, "expression": domRegistryExpression, "returnByValue": false,
	})
	if err != nil {
		return nil, err
	}
	object, err := decodeRemote(result)
	if err != nil {
		return nil, err
	}
	if object.ObjectID == "" {
		return nil, fmt.Errorf("DOM registry has no remote object")
	}
	document := &domDocument{sessionID: sessionID, contextID: contextID, objectID: object.ObjectID, localIDs: make(map[int]int)}
	b.domDocuments[key] = document
	return document, nil
}

func (b *Bridge) callRegistry(document *domDocument, declaration string, args []map[string]interface{}, byValue bool) (remoteObject, error) {
	arguments := []map[string]interface{}{{"objectId": document.objectID}}
	arguments = append(arguments, args...)
	result, err := b.callJuggler(document.sessionID, "Runtime.callFunction", map[string]interface{}{
		"executionContextId":  document.contextID,
		"functionDeclaration": declaration,
		"args":                arguments, "returnByValue": byValue,
	})
	if err != nil {
		return remoteObject{}, err
	}
	object, err := decodeRemote(result)
	if err == nil && object.ObjectID != "" {
		b.nodeObjectsMu.Lock()
		b.objectOwners[object.ObjectID] = document.sessionID
		b.objectContexts[object.ObjectID] = document.contextID
		b.nodeObjectsMu.Unlock()
	}
	return object, err
}

func (b *Bridge) registerDOMTree(document *domDocument, node map[string]interface{}) {
	localID, _ := node["localId"].(float64)
	b.domMu.Lock()
	backendID := document.localIDs[int(localID)]
	if backendID == 0 {
		backendID = b.nextCtxID()
		document.localIDs[int(localID)] = backendID
		b.domNodes[backendID] = domNodeRef{document: document, localID: int(localID)}
	}
	b.domMu.Unlock()
	delete(node, "localId")
	node["nodeId"] = backendID
	node["backendNodeId"] = backendID
	for _, field := range []string{"children", "shadowRoots"} {
		children, _ := node[field].([]interface{})
		for _, child := range children {
			if childNode, ok := child.(map[string]interface{}); ok {
				b.registerDOMTree(document, childNode)
			}
		}
	}
}

func (b *Bridge) describeDOM(document *domDocument, localID, depth int) (map[string]interface{}, error) {
	object, err := b.callRegistry(document, `function(registry, localId, depth) {
		return registry.describe(localId ? registry.node(localId) : registry.document, depth);
	}`, []map[string]interface{}{{"value": localID}, {"value": depth}}, true)
	if err != nil {
		return nil, err
	}
	var node map[string]interface{}
	if err := json.Unmarshal(object.Value, &node); err != nil {
		return nil, err
	}
	if node == nil {
		return nil, fmt.Errorf("browser returned no DOM node")
	}
	b.registerDOMTree(document, node)
	return node, nil
}

func (b *Bridge) domReference(sessionID string, nodeID int) (domNodeRef, error) {
	b.domMu.Lock()
	reference, ok := b.domNodes[nodeID]
	b.domMu.Unlock()
	if !ok || reference.document.sessionID != sessionID {
		return domNodeRef{}, fmt.Errorf("node not found")
	}
	return reference, nil
}

func (b *Bridge) domObject(sessionID string, nodeID int) (remoteObject, error) {
	reference, err := b.domReference(sessionID, nodeID)
	if err != nil {
		return remoteObject{}, err
	}
	return b.callRegistry(reference.document, `function(registry, localId) { return registry.node(localId); }`, []map[string]interface{}{{"value": reference.localID}}, false)
}

func (b *Bridge) referenceForObject(sessionID, objectID string) (domNodeRef, error) {
	b.nodeObjectsMu.RLock()
	owner := b.objectOwners[objectID]
	contextID := b.objectContexts[objectID]
	b.nodeObjectsMu.RUnlock()
	if owner != "" && owner != sessionID {
		return domNodeRef{}, fmt.Errorf("object not found")
	}
	document, err := b.domDocumentFor(sessionID, contextID)
	if err != nil {
		return domNodeRef{}, err
	}
	if contextID != "" && contextID != document.contextID {
		adopted, err := b.callJuggler(sessionID, "Page.adoptNode", map[string]interface{}{
			"frameId": b.frameIDForContext(sessionID, contextID), "objectId": objectID, "executionContextId": document.contextID,
		})
		if err != nil {
			return domNodeRef{}, err
		}
		var response struct {
			RemoteObject *remoteObject `json:"remoteObject"`
		}
		if err := json.Unmarshal(adopted, &response); err != nil {
			return domNodeRef{}, err
		}
		if response.RemoteObject == nil || response.RemoteObject.ObjectID == "" {
			return domNodeRef{}, fmt.Errorf("cannot adopt DOM node into document context")
		}
		objectID = response.RemoteObject.ObjectID
		defer b.callJuggler(sessionID, "Runtime.disposeObject", map[string]interface{}{"executionContextId": document.contextID, "objectId": objectID})
	}
	object, err := b.callRegistry(document, `function(registry, node) { return registry.id(node); }`, []map[string]interface{}{{"objectId": objectID}}, true)
	if err != nil {
		return domNodeRef{}, err
	}
	var localID int
	if err := json.Unmarshal(object.Value, &localID); err != nil {
		return domNodeRef{}, err
	}
	return domNodeRef{document: document, localID: localID}, nil
}

func (b *Bridge) clearDOMSession(sessionID string) {
	b.clearDOMContext(sessionID, "")
}

func (b *Bridge) clearDOMContext(sessionID, contextID string) {
	b.domMu.Lock()
	for key, document := range b.domDocuments {
		if document.sessionID == sessionID && (contextID == "" || document.contextID == contextID) {
			delete(b.domDocuments, key)
		}
	}
	for id, reference := range b.domNodes {
		if reference.document.sessionID == sessionID && (contextID == "" || reference.document.contextID == contextID) {
			delete(b.domNodes, id)
		}
	}
	b.domMu.Unlock()
	b.nodeObjectsMu.Lock()
	for objectID, owner := range b.objectOwners {
		if owner == sessionID && (contextID == "" || b.objectContexts[objectID] == contextID) {
			delete(b.objectOwners, objectID)
			delete(b.objectContexts, objectID)
		}
	}
	b.nodeObjectsMu.Unlock()
}
