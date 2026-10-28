package bridge

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/VulpineOS/foxbridge/pkg/cdp"
)

type domNodeParams struct {
	NodeID             int      `json:"nodeId"`
	BackendNodeID      int      `json:"backendNodeId"`
	ObjectID           string   `json:"objectId"`
	Depth              int      `json:"depth"`
	Selector           string   `json:"selector"`
	Name               string   `json:"name"`
	Value              string   `json:"value"`
	OuterHTML          string   `json:"outerHTML"`
	Files              []string `json:"files"`
	FrameID            string   `json:"frameId"`
	ExecutionContextID int      `json:"executionContextId"`
}

func (b *Bridge) callDOMFunction(sessionID string, params map[string]interface{}) (json.RawMessage, error) {
	objectID, _ := params["objectId"].(string)
	declaration, _ := params["functionDeclaration"].(string)
	b.nodeObjectsMu.RLock()
	owner := b.objectOwners[objectID]
	contextID := b.objectContexts[objectID]
	b.nodeObjectsMu.RUnlock()
	if owner != "" && owner != sessionID {
		return nil, fmt.Errorf("object not found")
	}
	if contextID == "" {
		contextID = b.latestContextForSession(sessionID)
	}
	delete(params, "objectId")
	params["functionDeclaration"] = fmt.Sprintf(`function(node) { return (%s).call(node); }`, declaration)
	params["args"] = []map[string]string{{"objectId": objectID}}
	params["executionContextId"] = contextID
	result, err := b.callJuggler(sessionID, "Runtime.callFunction", params)
	if err != nil {
		return nil, err
	}
	_, err = decodeRemote(result)
	return result, err
}

func (b *Bridge) handleDOM(conn *cdp.Connection, msg *cdp.Message) (json.RawMessage, *cdp.Error) {
	if msg.Method == "DOM.enable" || msg.Method == "DOM.disable" {
		return json.RawMessage(`{}`), nil
	}
	var params domNodeParams
	if len(msg.Params) > 0 && json.Unmarshal(msg.Params, &params) != nil {
		return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
	}
	result, err := b.domOperation(msg.SessionID, msg.Method, params)
	if err != nil {
		if strings.HasPrefix(err.Error(), "method not found:") {
			return nil, &cdp.Error{Code: -32601, Message: err.Error()}
		}
		return nil, &cdp.Error{Code: -32000, Message: err.Error()}
	}
	return result, nil
}

func (b *Bridge) domOperation(sessionID, method string, params domNodeParams) (json.RawMessage, error) {
	if method == "DOM.getDocument" {
		document, err := b.domDocumentFor(sessionID, "")
		if err != nil {
			return nil, err
		}
		depth := params.Depth
		if depth == 0 {
			depth = 1
		}
		node, err := b.describeDOM(document, 0, depth)
		if err != nil {
			return nil, err
		}
		result, _ := json.Marshal(map[string]interface{}{"root": node})
		return result, nil
	}

	if method == "DOM.getFrameOwner" {
		return b.frameOwner(sessionID, params.FrameID)
	}

	known := map[string]bool{
		"DOM.describeNode": true, "Page.describeNode": true, "DOM.resolveNode": true,
		"DOM.requestNode": true, "DOM.querySelector": true, "DOM.querySelectorAll": true,
		"DOM.getBoxModel": true, "DOM.getContentQuads": true, "DOM.scrollIntoViewIfNeeded": true,
		"DOM.focus": true, "DOM.getAttributes": true, "DOM.getOuterHTML": true,
		"DOM.setAttributeValue": true, "DOM.setNodeValue": true, "DOM.setOuterHTML": true,
		"DOM.removeNode": true, "DOM.setFileInputFiles": true,
	}
	if !known[method] {
		return nil, fmt.Errorf("method not found: %s", method)
	}
	var reference domNodeRef
	var err error
	if params.ObjectID != "" {
		reference, err = b.referenceForObject(sessionID, params.ObjectID)
	} else {
		nodeID := params.BackendNodeID
		if nodeID == 0 {
			nodeID = params.NodeID
		}
		reference, err = b.domReference(sessionID, nodeID)
	}
	if err != nil {
		return nil, err
	}

	switch method {
	case "DOM.describeNode", "Page.describeNode", "DOM.requestNode":
		node, err := b.describeDOM(reference.document, reference.localID, params.Depth)
		if err != nil {
			return nil, err
		}
		if method == "DOM.requestNode" {
			return json.Marshal(map[string]interface{}{"nodeId": node["nodeId"]})
		}
		if node["nodeName"] == "IFRAME" || node["nodeName"] == "FRAME" {
			object, err := b.callRegistry(reference.document, `function(registry, id) { return registry.node(id); }`, []map[string]interface{}{{"value": reference.localID}}, false)
			if err != nil {
				return nil, err
			}
			defer b.callJuggler(sessionID, "Runtime.disposeObject", map[string]interface{}{"executionContextId": reference.document.contextID, "objectId": object.ObjectID})
			result, err := b.callJuggler(sessionID, "Page.describeNode", map[string]interface{}{"frameId": b.frameIDForContext(reference.document.contextID), "objectId": object.ObjectID})
			if err != nil {
				return nil, err
			}
			var frame struct {
				ContentFrameID string `json:"contentFrameId"`
			}
			if err := json.Unmarshal(result, &frame); err != nil {
				return nil, err
			}
			if frame.ContentFrameID != "" {
				node["frameId"] = b.cdpFrameIDForSession(sessionID, frame.ContentFrameID)
				node["contentFrameId"] = frame.ContentFrameID
			}
		}
		return json.Marshal(map[string]interface{}{"node": node})
	case "DOM.resolveNode":
		object, err := b.callRegistry(reference.document, `function(registry, id) { return registry.node(id); }`, []map[string]interface{}{{"value": reference.localID}}, false)
		if err != nil {
			return nil, err
		}
		if params.ExecutionContextID != 0 {
			b.ctxMapMu.RLock()
			contextID := b.ctxMap[params.ExecutionContextID]
			owner := b.ctxOwners[params.ExecutionContextID]
			b.ctxMapMu.RUnlock()
			if contextID == "" || owner != sessionID {
				return nil, fmt.Errorf("execution context not found")
			}
			if contextID != reference.document.contextID {
				original := object.ObjectID
				defer b.callJuggler(sessionID, "Runtime.disposeObject", map[string]interface{}{"executionContextId": reference.document.contextID, "objectId": original})
				adopted, err := b.callJuggler(sessionID, "Page.adoptNode", map[string]interface{}{"frameId": b.frameIDForContext(reference.document.contextID), "objectId": original, "executionContextId": contextID})
				if err != nil {
					return nil, err
				}
				var response struct {
					RemoteObject *remoteObject `json:"remoteObject"`
				}
				if err := json.Unmarshal(adopted, &response); err != nil {
					return nil, err
				}
				if response.RemoteObject == nil {
					return nil, fmt.Errorf("cannot adopt DOM node into requested context")
				}
				object = *response.RemoteObject
				b.rememberRuntimeObject(sessionID, contextID, mustMarshal(map[string]interface{}{"result": object}))
			}
		}
		return json.Marshal(map[string]interface{}{"object": object})
	case "DOM.querySelector", "DOM.querySelectorAll":
		object, err := b.callRegistry(reference.document, `function(registry, id, selector, all) {
			const root = registry.node(id);
			return Array.from(all ? root.querySelectorAll(selector) : [root.querySelector(selector)]).filter(Boolean).map(node => registry.describe(node, 0));
		}`, []map[string]interface{}{{"value": reference.localID}, {"value": params.Selector}, {"value": method == "DOM.querySelectorAll"}}, true)
		if err != nil {
			return nil, err
		}
		var nodes []map[string]interface{}
		if err := json.Unmarshal(object.Value, &nodes); err != nil {
			return nil, err
		}
		ids := make([]int, 0, len(nodes))
		for _, node := range nodes {
			b.registerDOMTree(reference.document, node)
			ids = append(ids, node["nodeId"].(int))
		}
		if method == "DOM.querySelectorAll" {
			return json.Marshal(map[string]interface{}{"nodeIds": ids})
		}
		id := 0
		if len(ids) > 0 {
			id = ids[0]
		}
		return json.Marshal(map[string]interface{}{"nodeId": id})
	}

	object, err := b.callRegistry(reference.document, `function(registry, id) { return registry.node(id); }`, []map[string]interface{}{{"value": reference.localID}}, false)
	if err != nil {
		return nil, err
	}
	defer b.callJuggler(sessionID, "Runtime.disposeObject", map[string]interface{}{"executionContextId": reference.document.contextID, "objectId": object.ObjectID})
	var declaration string
	switch method {
	case "DOM.getBoxModel", "DOM.getContentQuads":
		result, err := b.callJuggler(sessionID, "Page.getContentQuads", map[string]interface{}{"frameId": b.frameIDForContext(reference.document.contextID), "objectId": object.ObjectID})
		if err != nil {
			return nil, err
		}
		var geometry struct {
			Quads []struct{ P1, P2, P3, P4 struct{ X, Y float64 } } `json:"quads"`
		}
		if err := json.Unmarshal(result, &geometry); err != nil {
			return nil, err
		}
		if len(geometry.Quads) == 0 {
			return nil, fmt.Errorf("node has no layout")
		}
		quads := make([][]float64, 0, len(geometry.Quads))
		for _, quad := range geometry.Quads {
			quads = append(quads, []float64{quad.P1.X, quad.P1.Y, quad.P2.X, quad.P2.Y, quad.P3.X, quad.P3.Y, quad.P4.X, quad.P4.Y})
		}
		if method == "DOM.getContentQuads" {
			return json.Marshal(map[string]interface{}{"quads": quads})
		}
		dimensionsResult, err := b.callDOMFunction(sessionID, map[string]interface{}{"objectId": object.ObjectID, "returnByValue": true, "functionDeclaration": `function() {
			if (typeof this.offsetWidth === 'number') return {width: this.offsetWidth, height: this.offsetHeight};
			if (this instanceof SVGSVGElement && !this.ownerSVGElement) {
				const style = getComputedStyle(this);
				const horizontal = parseFloat(style.paddingLeft) + parseFloat(style.paddingRight) + parseFloat(style.borderLeftWidth) + parseFloat(style.borderRightWidth);
				const vertical = parseFloat(style.paddingTop) + parseFloat(style.paddingBottom) + parseFloat(style.borderTopWidth) + parseFloat(style.borderBottomWidth);
				const width = parseFloat(style.width);
				const height = parseFloat(style.height);
				return {
					width: Math.round(style.boxSizing === 'border-box' ? Math.max(width, horizontal) : width + horizontal),
					height: Math.round(style.boxSizing === 'border-box' ? Math.max(height, vertical) : height + vertical)
				};
			}
			const bounds = this.getBBox();
			return {width: Math.round(bounds.width), height: Math.round(bounds.height)};
		}`})
		if err != nil {
			return nil, err
		}
		dimensions, err := decodeRemote(dimensionsResult)
		if err != nil {
			return nil, err
		}
		var size struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		}
		if err := json.Unmarshal(dimensions.Value, &size); err != nil {
			return nil, err
		}
		content := quads[0]
		return json.Marshal(map[string]interface{}{"model": map[string]interface{}{"content": content, "padding": content, "border": content, "margin": content, "width": size.Width, "height": size.Height}})
	case "DOM.scrollIntoViewIfNeeded":
		// Firefox can apply a preceding focus operation's scroll on the next paint.
		// Let it settle before scrolling this target, or it can undo our scroll
		// between measuring the element and dispatching the pointer event.
		_, err := b.callDOMFunction(sessionID, map[string]interface{}{"objectId": object.ObjectID, "returnByValue": true, "functionDeclaration": `async function() {
			await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
		}`})
		if err != nil {
			return nil, err
		}
		return b.callJuggler(sessionID, "Page.scrollIntoViewIfNeeded", map[string]interface{}{"frameId": b.frameIDForContext(reference.document.contextID), "objectId": object.ObjectID})
	case "DOM.focus":
		declaration = `function() { this.focus(); }`
	case "DOM.getAttributes":
		declaration = `function() { return Array.from(this.attributes || []).flatMap(attribute => [attribute.name, attribute.value]); }`
	case "DOM.getOuterHTML":
		declaration = `function() { return this.outerHTML; }`
	case "DOM.setAttributeValue":
		declaration = fmt.Sprintf(`function() { this.setAttribute(%q, %q); }`, params.Name, params.Value)
	case "DOM.setNodeValue":
		declaration = fmt.Sprintf(`function() { this.nodeValue = %q; }`, params.Value)
	case "DOM.setOuterHTML":
		declaration = fmt.Sprintf(`function() { this.outerHTML = %q; }`, params.OuterHTML)
	case "DOM.removeNode":
		declaration = `function() { this.remove(); }`
	case "DOM.setFileInputFiles":
		return b.callJuggler(sessionID, "Page.setFileInputFiles", map[string]interface{}{"objectId": object.ObjectID, "files": params.Files})
	}
	result, err := b.callDOMFunction(sessionID, map[string]interface{}{"objectId": object.ObjectID, "functionDeclaration": declaration, "returnByValue": true})
	if err != nil {
		return nil, err
	}
	value, err := decodeRemote(result)
	if err != nil {
		return nil, err
	}
	switch method {
	case "DOM.getAttributes":
		return json.Marshal(map[string]interface{}{"attributes": value.Value})
	case "DOM.getOuterHTML":
		return json.Marshal(map[string]interface{}{"outerHTML": value.Value})
	default:
		return json.RawMessage(`{}`), nil
	}
}

func (b *Bridge) frameOwner(sessionID, frameID string) (json.RawMessage, error) {
	document, err := b.domDocumentFor(sessionID, "")
	if err != nil {
		return nil, err
	}
	root, err := b.describeDOM(document, 0, 1)
	if err != nil {
		return nil, err
	}
	result, err := b.domOperation(sessionID, "DOM.querySelectorAll", domNodeParams{NodeID: root["nodeId"].(int), Selector: "iframe,frame"})
	if err != nil {
		return nil, err
	}
	var matches struct {
		NodeIDs []int `json:"nodeIds"`
	}
	if err := json.Unmarshal(result, &matches); err != nil {
		return nil, err
	}
	for _, nodeID := range matches.NodeIDs {
		object, err := b.domObject(sessionID, nodeID)
		if err != nil {
			return nil, err
		}
		var frame struct {
			ContentFrameID string `json:"contentFrameId"`
		}
		description, err := b.callJuggler(sessionID, "Page.describeNode", map[string]interface{}{"objectId": object.ObjectID, "frameId": b.frameIDForContext(document.contextID)})
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(description, &frame); err != nil {
			return nil, err
		}
		if frame.ContentFrameID == frameID {
			return json.Marshal(map[string]interface{}{"backendNodeId": nodeID, "nodeId": nodeID})
		}
	}
	return nil, fmt.Errorf("frame owner not found")
}
