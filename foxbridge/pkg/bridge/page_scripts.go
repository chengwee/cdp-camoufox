package bridge

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/chengwee/cdp-camoufox/foxbridge/pkg/cdp"
)

type pageInitScript struct {
	ID        string `json:"-"`
	Script    string `json:"script"`
	WorldName string `json:"worldName,omitempty"`
}

func contextType(name string) string {
	if name == "" {
		return "default"
	}
	return "isolated"
}

func (b *Bridge) contextForWorld(sessionID, frameID, name string) (int, string) {
	b.ctxMapMu.RLock()
	defer b.ctxMapMu.RUnlock()
	var numericID int
	var contextID string
	for id, candidate := range b.ctxMap {
		key := scopedContextID(sessionID, candidate)
		if id > numericID && b.ctxOwners[id] == sessionID && b.contextFrames[key] == frameID && b.contextNames[key] == name {
			numericID, contextID = id, candidate
		}
	}
	return numericID, contextID
}

func (b *Bridge) handlePageScripts(msg *cdp.Message) (json.RawMessage, *cdp.Error) {
	var params struct {
		FrameID    string `json:"frameId"`
		WorldName  string `json:"worldName"`
		Source     string `json:"source"`
		Identifier string `json:"identifier"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
	}
	if msg.Method == "Page.createIsolatedWorld" {
		params.FrameID = b.jugglerFrameIDForSession(msg.SessionID, params.FrameID)
		if id, _ := b.contextForWorld(msg.SessionID, params.FrameID, params.WorldName); id != 0 {
			return marshalResult(map[string]interface{}{"executionContextId": id})
		}
	}
	b.initScriptsMu.Lock()
	scripts := append([]pageInitScript{}, b.initScripts[msg.SessionID]...)
	identifier := fmt.Sprintf("script-%d", b.nextCtxID())
	if msg.Method == "Page.removeScriptToEvaluateOnNewDocument" {
		retained := scripts[:0]
		for _, script := range scripts {
			if script.ID != params.Identifier {
				retained = append(retained, script)
			}
		}
		scripts = retained
	} else {
		present := false
		if msg.Method == "Page.createIsolatedWorld" {
			for _, script := range scripts {
				if script.WorldName == params.WorldName {
					present = true
					break
				}
			}
		}
		if !present {
			scripts = append(scripts, pageInitScript{ID: identifier, Script: params.Source, WorldName: params.WorldName})
		}
	}
	_, err := b.callJuggler(msg.SessionID, "Page.setInitScripts", map[string]interface{}{"scripts": scripts})
	if err == nil {
		b.initScripts[msg.SessionID] = scripts
	}
	b.initScriptsMu.Unlock()
	if err != nil {
		return nil, &cdp.Error{Code: -32000, Message: err.Error()}
	}
	if msg.Method != "Page.createIsolatedWorld" {
		if msg.Method == "Page.removeScriptToEvaluateOnNewDocument" {
			return json.RawMessage(`{}`), nil
		}
		return marshalResult(map[string]interface{}{"identifier": identifier})
	}
	deadline := time.Now().Add(executionContextTimeout)
	for time.Now().Before(deadline) {
		if id, _ := b.contextForWorld(msg.SessionID, params.FrameID, params.WorldName); id != 0 {
			return marshalResult(map[string]interface{}{"executionContextId": id})
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil, &cdp.Error{Code: -32000, Message: "isolated execution context not available"}
}

func (b *Bridge) contextForObjects(sessionID, objectID string, rawArgs json.RawMessage) (string, error) {
	var args []struct {
		ObjectID string `json:"objectId"`
	}
	if len(rawArgs) != 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", err
		}
	}
	args = append(args, struct {
		ObjectID string `json:"objectId"`
	}{objectID})
	contextID := ""
	b.nodeObjectsMu.RLock()
	defer b.nodeObjectsMu.RUnlock()
	for _, arg := range args {
		if arg.ObjectID == "" {
			continue
		}
		if owner := b.objectOwners[arg.ObjectID]; owner != "" && owner != sessionID {
			return "", fmt.Errorf("object not found")
		}
		candidate := b.objectContexts[arg.ObjectID]
		if candidate != "" && contextID != "" && candidate != contextID {
			return "", fmt.Errorf("objects belong to different execution contexts")
		}
		if candidate != "" {
			contextID = candidate
		}
	}
	return contextID, nil
}
