package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/VulpineOS/foxbridge/pkg/cdp"
)

func (b *Bridge) handleAccessibility(conn *cdp.Connection, msg *cdp.Message) (json.RawMessage, *cdp.Error) {
	switch msg.Method {
	case "Accessibility.enable", "Accessibility.disable":
		return json.RawMessage(`{}`), nil

	case "Accessibility.getFullAXTree":
		var params struct {
			FrameID string `json:"frameId"`
		}
		if len(msg.Params) != 0 && json.Unmarshal(msg.Params, &params) != nil {
			return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
		}
		if !b.isBiDi && params.FrameID != "" {
			if info, ok := b.sessions.Get(msg.SessionID); ok && b.jugglerFrameIDForSession(msg.SessionID, params.FrameID) != info.FrameID {
				return nil, &cdp.Error{Code: -32000, Message: "frame-scoped accessibility is not supported by this Camoufox Juggler protocol"}
			}
		}
		result, err := b.captureAccessibility(msg.SessionID)
		if err != nil {
			return nil, &cdp.Error{Code: -32000, Message: err.Error()}
		}
		adapted, err := adaptAccessibilityTreeResult(result)
		if err != nil {
			return nil, &cdp.Error{Code: -32000, Message: err.Error()}
		}
		return adapted, nil

	default:
		return nil, &cdp.Error{Code: -32601, Message: fmt.Sprintf("method not found: %s", msg.Method)}
	}
}

func (b *Bridge) captureAccessibility(sessionID string) (json.RawMessage, error) {
	deadline := time.Now().Add(5 * time.Second)
	for attempt := 0; ; attempt++ {
		var document *domDocument
		var version int
		var err error
		if !b.isBiDi && b.latestContextForSession(sessionID) != "" {
			document, err = b.domDocumentFor(sessionID, "")
			if err == nil {
				version, err = b.domVersion(document)
			}
			if err != nil {
				return nil, err
			}
		}
		result, err := b.callJuggler(sessionID, "Accessibility.getFullAXTree", nil)
		if err == nil && document != nil {
			result, err = b.associateAccessibilityNodes(sessionID, document, version, result)
		}
		if !errors.Is(err, errAccessibilityChanged) || attempt >= 2 || time.Now().After(deadline) {
			return result, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func adaptAccessibilityTreeResult(result json.RawMessage) (json.RawMessage, error) {
	var cdpResult struct {
		Nodes json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(result, &cdpResult); err == nil && len(cdpResult.Nodes) > 0 && string(cdpResult.Nodes) != "null" {
		return result, nil
	}

	var jugglerResult struct {
		Tree map[string]json.RawMessage `json:"tree"`
	}
	if err := json.Unmarshal(result, &jugglerResult); err != nil {
		return nil, fmt.Errorf("decode accessibility tree: %w", err)
	}
	if jugglerResult.Tree == nil {
		return json.RawMessage(`{"nodes":[]}`), nil
	}

	nodes := make([]map[string]interface{}, 0)
	appendAXNode(jugglerResult.Tree, &nodes)
	return json.Marshal(map[string]interface{}{"nodes": nodes})
}

func appendAXNode(raw map[string]json.RawMessage, nodes *[]map[string]interface{}) string {
	nodeID := strconv.Itoa(len(*nodes) + 1)
	node := map[string]interface{}{
		"nodeId":  nodeID,
		"ignored": false,
	}
	if backendID, ok := raw["backendDOMNodeId"]; ok {
		node["backendDOMNodeId"] = backendID
	}

	if role := stringAXField(raw, "role"); role != "" {
		node["role"] = axValue("role", normalizeAXRole(role))
	}
	node["name"] = axValue("computedString", stringAXField(raw, "name"))
	if value, ok := rawAXValue(raw, "value"); ok {
		node["value"] = value
	}
	if description := stringAXField(raw, "description"); description != "" {
		node["description"] = axValue("computedString", description)
	}

	nodeIndex := len(*nodes)
	*nodes = append(*nodes, node)

	var children []map[string]json.RawMessage
	if data, ok := raw["children"]; ok {
		_ = json.Unmarshal(data, &children)
	}
	if len(children) > 0 {
		childIDs := make([]string, 0, len(children))
		for _, child := range children {
			childIDs = append(childIDs, appendAXNode(child, nodes))
		}
		node["childIds"] = childIDs
	}

	if properties := axProperties(raw); len(properties) > 0 {
		node["properties"] = properties
	}
	(*nodes)[nodeIndex] = node
	return nodeID
}

func stringAXField(raw map[string]json.RawMessage, name string) string {
	var value string
	_ = json.Unmarshal(raw[name], &value)
	return value
}

func rawAXValue(raw map[string]json.RawMessage, name string) (map[string]interface{}, bool) {
	data, ok := raw[name]
	if !ok || len(data) == 0 || string(data) == "null" {
		return nil, false
	}
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return axValue(axValueType(value), normalizeJSONNumber(value)), true
}

func axProperties(raw map[string]json.RawMessage) []map[string]interface{} {
	skip := map[string]bool{
		"role": true, "name": true, "value": true, "description": true,
		"children": true, "foundObject": true, "backendDOMNodeId": true,
	}
	properties := make([]map[string]interface{}, 0)
	for name, data := range raw {
		if skip[name] || len(data) == 0 || string(data) == "null" {
			continue
		}
		var value interface{}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			continue
		}
		properties = append(properties, map[string]interface{}{
			"name":  name,
			"value": axValue(axValueType(value), normalizeJSONNumber(value)),
		})
	}
	return properties
}

func axValue(valueType string, value interface{}) map[string]interface{} {
	return map[string]interface{}{
		"type":  valueType,
		"value": value,
	}
}

func axValueType(value interface{}) string {
	switch value.(type) {
	case bool:
		return "boolean"
	case float64, json.Number:
		return "number"
	default:
		return "string"
	}
}

func normalizeJSONNumber(value interface{}) interface{} {
	if n, ok := value.(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return f
		}
	}
	return value
}

func normalizeAXRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "document":
		return "RootWebArea"
	case "pushbutton":
		return "button"
	case "entry":
		return "textbox"
	case "text leaf":
		return "StaticText"
	case "checkbutton":
		return "checkbox"
	case "radiobutton":
		return "radio"
	default:
		return role
	}
}
