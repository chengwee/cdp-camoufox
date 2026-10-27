package bridge

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/VulpineOS/foxbridge/pkg/cdp"
)

func mustMarshal(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func (b *Bridge) handleRuntime(conn *cdp.Connection, msg *cdp.Message) (json.RawMessage, *cdp.Error) {
	switch msg.Method {
	case "Runtime.enable":
		// Emit the latest execution context for this session so Puppeteer can
		// start evaluating immediately. The context was created by BiDi's
		// script.realmCreated or Juggler's Runtime.executionContextCreated.
		go func() {
			frameID := ""
			if info, ok := b.sessions.Get(msg.SessionID); ok {
				frameID = info.FrameID
			}
			frameID = b.cdpFrameIDForSession(msg.SessionID, frameID)
			jugglerSessionID := b.resolveSession(msg.SessionID)
			b.latestCtxMu.RLock()
			latestCtx := b.latestCtx[jugglerSessionID]
			b.latestCtxMu.RUnlock()

			if frameID != "" && latestCtx != "" {
				ctxID := b.nextCtxID()
				b.ctxMapMu.Lock()
				b.ctxMap[ctxID] = latestCtx
				b.ctxOwners[ctxID] = msg.SessionID
				b.ctxMapMu.Unlock()

				b.emitEvent("Runtime.executionContextCreated", map[string]interface{}{
					"context": map[string]interface{}{
						"id":       ctxID,
						"origin":   "",
						"name":     "",
						"uniqueId": latestCtx,
						"auxData": map[string]interface{}{
							"isDefault": true,
							"type":      "default",
							"frameId":   frameID,
						},
					},
				}, msg.SessionID)
			}
		}()
		return json.RawMessage(`{}`), nil

	case "Runtime.evaluate":
		log.Printf("[runtime] evaluate on session=%s params=%s", msg.SessionID, string(msg.Params)[:min(len(msg.Params), 200)])
		var params struct {
			Expression            string `json:"expression"`
			ReturnByValue         bool   `json:"returnByValue"`
			AwaitPromise          bool   `json:"awaitPromise"`
			UniqueContextID       string `json:"uniqueContextId"`
			ContextID             int    `json:"contextId"`
			GeneratePreview       bool   `json:"generatePreview"`
			UserGesture           bool   `json:"userGesture"`
			IncludeCommandLineAPI bool   `json:"includeCommandLineAPI"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
		}

		if !b.uniqueContextOwned(msg.SessionID, params.UniqueContextID) {
			return nil, &cdp.Error{Code: -32000, Message: "execution context not found"}
		}

		// Map CDP contextId (numeric) to Juggler executionContextId (string)
		if params.ContextID > 0 && !b.contextOwned(msg.SessionID, params.ContextID) {
			return nil, &cdp.Error{Code: -32000, Message: "execution context not found"}
		}
		execCtxID := params.UniqueContextID
		if execCtxID == "" && params.ContextID > 0 {
			b.ctxMapMu.RLock()
			if mapped, ok := b.ctxMap[params.ContextID]; ok {
				execCtxID = mapped
			}
			b.ctxMapMu.RUnlock()
			if execCtxID == "" {
				return nil, &cdp.Error{Code: -32000, Message: "execution context not found"}
			}
		}

		if execCtxID == "" {
			execCtxID = b.latestContextForSession(msg.SessionID)
		}

		// A session that has just attached may not have seen its first
		// Runtime.executionContextCreated yet. Juggler rejects the call outright
		// when executionContextId is missing ("Expected <root>.executionContextId
		// to be |string|"), so wait for it rather than sending an unusable call.
		// Session-less calls have no context to wait on and keep their historical
		// pass-through behaviour.
		if execCtxID == "" && msg.SessionID != "" {
			ctxID, ok := b.waitForContext(msg.SessionID, "", executionContextTimeout)
			if !ok {
				return nil, &cdp.Error{Code: -32000, Message: "execution context not available for session"}
			}
			execCtxID = ctxID
		}

		// If awaitPromise is requested, wrap the expression so the promise is resolved
		// before returning. Juggler's Runtime.evaluate doesn't support awaitPromise natively.
		expression := params.Expression
		if params.AwaitPromise {
			// Wrap in an async IIFE that awaits the result. Juggler will evaluate
			// this synchronously, but the await inside handles promise resolution.
			// We use a special wrapper that Juggler can handle.
			expression = fmt.Sprintf(`(async () => { return await (%s) })()`, expression)
		}

		// Juggler only accepts: executionContextId, expression, returnByValue
		jugglerParams := map[string]interface{}{
			"expression":    expression,
			"returnByValue": params.ReturnByValue,
		}
		if execCtxID != "" {
			jugglerParams["executionContextId"] = execCtxID
		}

		log.Printf("[runtime] calling Juggler Runtime.evaluate with %v", jugglerParams)
		result, err := b.callJuggler(msg.SessionID, "Runtime.evaluate", jugglerParams)
		if err != nil {
			log.Printf("[runtime] evaluate error: %v", err)
			return nil, &cdp.Error{Code: -32000, Message: err.Error()}
		}

		log.Printf("[runtime] evaluate result: %s", string(result)[:min(len(result), 300)])
		b.rememberRuntimeObject(msg.SessionID, execCtxID, result)
		return normalizeRuntimeResult(result), nil

	case "Runtime.callFunctionOn":
		log.Printf("[runtime] callFunctionOn params: %s", string(msg.Params)[:min(len(msg.Params), 500)])
		var params struct {
			FunctionDeclaration string          `json:"functionDeclaration"`
			ObjectID            string          `json:"objectId"`
			Arguments           json.RawMessage `json:"arguments"`
			ReturnByValue       bool            `json:"returnByValue"`
			AwaitPromise        bool            `json:"awaitPromise"`
			ExecutionContextID  int             `json:"executionContextId"`
			UniqueContextID     string          `json:"uniqueContextId"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
		}
		if !b.uniqueContextOwned(msg.SessionID, params.UniqueContextID) {
			return nil, &cdp.Error{Code: -32000, Message: "execution context not found"}
		}
		if params.ObjectID != "" {
			b.nodeObjectsMu.RLock()
			owner := b.objectOwners[params.ObjectID]
			b.nodeObjectsMu.RUnlock()
			if owner != "" && owner != msg.SessionID {
				return nil, &cdp.Error{Code: -32000, Message: "object not found"}
			}
		}

		// Map CDP contextId to Juggler executionContextId
		if params.ExecutionContextID > 0 && !b.contextOwned(msg.SessionID, params.ExecutionContextID) {
			return nil, &cdp.Error{Code: -32000, Message: "execution context not found"}
		}
		execCtxID := params.UniqueContextID
		if execCtxID == "" && params.ExecutionContextID > 0 {
			b.ctxMapMu.RLock()
			if mapped, ok := b.ctxMap[params.ExecutionContextID]; ok {
				execCtxID = mapped
			}
			b.ctxMapMu.RUnlock()
			if execCtxID == "" {
				return nil, &cdp.Error{Code: -32000, Message: "execution context not found"}
			}
		}

		if params.ObjectID == "" && execCtxID == "" {
			latest := b.latestContextForSession(msg.SessionID)
			if latest != "" {
				execCtxID = latest
			}
		}

		funcDecl := params.FunctionDeclaration

		// Strip //# sourceURL= comments that Puppeteer appends to function declarations.
		// These break SpiderMonkey's async wrapper when the comment is inside grouping parens.
		if idx := strings.Index(funcDecl, "\n//# sourceURL="); idx >= 0 {
			funcDecl = funcDecl[:idx]
		}
		if idx := strings.Index(funcDecl, "\n//@ sourceURL="); idx >= 0 {
			funcDecl = funcDecl[:idx]
		}

		// Handle objectId-as-this: Juggler doesn't support objectId for `this` binding.
		// Prepend objectId as first argument and wrap function to use it as `this` or first arg.
		finalArgs := params.Arguments
		if params.ObjectID != "" {
			var existingArgs []json.RawMessage
			if params.Arguments != nil {
				json.Unmarshal(params.Arguments, &existingArgs)
			}
			newArgs := make([]json.RawMessage, 0, len(existingArgs)+1)
			objArg, _ := json.Marshal(map[string]string{"objectId": params.ObjectID})
			newArgs = append(newArgs, objArg)
			newArgs = append(newArgs, existingArgs...)
			finalArgs = mustMarshal(newArgs)

			// Juggler lacks CDP's objectId-as-this binding. Preserve the original
			// argument list and only emulate the this-value on the wrapped call.
			funcDecl = fmt.Sprintf(`function(__this__, ...args) { const fn = %s; return fn.call(__this__, ...args); }`, funcDecl)
		}

		// Build Juggler params AFTER all funcDecl transformations
		jugglerParams := map[string]interface{}{
			"functionDeclaration": funcDecl,
			"returnByValue":       params.ReturnByValue,
		}
		// awaitPromise is only supported by BiDi's script.callFunction, not Juggler
		if b.isBiDi {
			jugglerParams["awaitPromise"] = params.AwaitPromise
		}

		// Juggler ALWAYS requires executionContextId.
		// When arguments contain objectIds, the objects are bound to a specific context.
		// Using the latest context would cause "JSHandles can be evaluated only in the
		// context they were created" errors. Honor the caller's requested context instead.
		hasObjectIdArgs := false
		if finalArgs != nil {
			hasObjectIdArgs = strings.Contains(string(finalArgs), `"objectId"`)
		}
		if hasObjectIdArgs || params.ObjectID != "" {
			if !b.isBiDi {
				contextID, err := b.contextForObjects(msg.SessionID, params.ObjectID, finalArgs)
				if err != nil {
					return nil, &cdp.Error{Code: -32000, Message: err.Error()}
				}
				if contextID == "" {
					contextID = execCtxID
				}
				if contextID == "" {
					contextID = b.latestContextForSession(msg.SessionID)
				}
				jugglerParams["executionContextId"] = contextID
			} else if execCtxID != "" {
				jugglerParams["executionContextId"] = execCtxID
			} else {
				if latest := b.latestContextForSession(msg.SessionID); latest != "" {
					jugglerParams["executionContextId"] = latest
				}
			}
		} else {
			if execCtxID != "" {
				jugglerParams["executionContextId"] = execCtxID
			} else if latest := b.latestContextForSession(msg.SessionID); latest != "" {
				jugglerParams["executionContextId"] = latest
			}
		}

		if finalArgs != nil {
			jugglerParams["args"] = json.RawMessage(finalArgs)
		}

		result, err := b.callJuggler(msg.SessionID, "Runtime.callFunction", jugglerParams)
		if err != nil {
			return nil, &cdp.Error{Code: -32000, Message: err.Error()}
		}

		contextID, _ := jugglerParams["executionContextId"].(string)
		b.rememberRuntimeObject(msg.SessionID, contextID, result)
		return normalizeRuntimeResult(result), nil

	case "Runtime.releaseObject":
		var params struct {
			ObjectID string `json:"objectId"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
		}

		// Skip releasing dummy/empty object IDs (from our $eval interception)
		if params.ObjectID == "" {
			return json.RawMessage(`{}`), nil
		}
		b.nodeObjectsMu.RLock()
		objectOwner := b.objectOwners[params.ObjectID]
		objectContext := b.objectContexts[params.ObjectID]
		b.nodeObjectsMu.RUnlock()
		if objectOwner != "" && objectOwner != msg.SessionID {
			return nil, &cdp.Error{Code: -32000, Message: "object not found"}
		}

		disposeParams := map[string]interface{}{
			"objectId": params.ObjectID,
		}
		if objectContext == "" {
			objectContext = b.latestContextForSession(msg.SessionID)
		}
		if objectContext != "" {
			disposeParams["executionContextId"] = objectContext
		}
		// Gracefully handle dispose errors — the object may already be gone
		b.callJuggler(msg.SessionID, "Runtime.disposeObject", disposeParams)
		b.nodeObjectsMu.Lock()
		delete(b.objectOwners, params.ObjectID)
		delete(b.objectContexts, params.ObjectID)
		b.nodeObjectsMu.Unlock()
		return json.RawMessage(`{}`), nil

	case "Runtime.getProperties":
		var params struct {
			ObjectID                 string `json:"objectId"`
			OwnProperties            bool   `json:"ownProperties"`
			GeneratePreview          bool   `json:"generatePreview"`
			AccessorPropertiesOnly   bool   `json:"accessorPropertiesOnly"`
			NonIndexedPropertiesOnly bool   `json:"nonIndexedPropertiesOnly"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
		}

		if params.ObjectID == "" {
			return marshalResult(map[string]interface{}{"result": []interface{}{}})
		}
		b.nodeObjectsMu.RLock()
		owner := b.objectOwners[params.ObjectID]
		contextID := b.objectContexts[params.ObjectID]
		b.nodeObjectsMu.RUnlock()
		if owner != "" && owner != msg.SessionID {
			return nil, &cdp.Error{Code: -32000, Message: "object not found"}
		}

		getPropsParams := map[string]interface{}{
			"objectId": params.ObjectID,
		}
		if contextID == "" {
			contextID = b.latestContextForSession(msg.SessionID)
		}
		if contextID != "" {
			getPropsParams["executionContextId"] = contextID
		}
		result, err := b.callJuggler(msg.SessionID, "Runtime.getObjectProperties", getPropsParams)
		if err != nil {
			return nil, &cdp.Error{Code: -32000, Message: err.Error()}
		}

		// Juggler returns {properties: [{name, value}]}, CDP expects
		// {result: [{name, value, configurable, enumerable, writable, isOwn}]}
		var jugglerProps struct {
			Properties []struct {
				Name  string          `json:"name"`
				Value json.RawMessage `json:"value"`
			} `json:"properties"`
		}
		if json.Unmarshal(result, &jugglerProps) == nil && jugglerProps.Properties != nil {
			cdpProps := make([]map[string]interface{}, 0, len(jugglerProps.Properties))
			for _, p := range jugglerProps.Properties {
				b.rememberRuntimeObject(msg.SessionID, contextID, mustMarshal(map[string]interface{}{"result": p.Value}))
				cdpProps = append(cdpProps, map[string]interface{}{
					"name":         p.Name,
					"value":        p.Value,
					"configurable": true,
					"enumerable":   true,
					"writable":     true,
					"isOwn":        true,
				})
			}
			resp, _ := json.Marshal(map[string]interface{}{
				"result": cdpProps,
			})
			return resp, nil
		}

		return result, nil

	case "Runtime.releaseObjectGroup":
		return json.RawMessage(`{}`), nil

	case "Runtime.runIfWaitingForDebugger":
		return json.RawMessage(`{}`), nil

	case "Runtime.addBinding":
		return json.RawMessage(`{}`), nil

	case "Runtime.discardConsoleEntries":
		return json.RawMessage(`{}`), nil

	default:
		return nil, &cdp.Error{Code: -32601, Message: fmt.Sprintf("method not found: %s", msg.Method)}
	}
}

func normalizeRuntimeResult(result json.RawMessage) json.RawMessage {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(result, &payload); err != nil {
		return result
	}
	raw, ok := payload["result"]
	if !ok || string(raw) == "null" {
		payload["result"] = json.RawMessage(`{"type":"undefined"}`)
		out, err := json.Marshal(payload)
		if err != nil {
			return result
		}
		return out
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return result
	}
	if _, ok := object["type"]; ok {
		return result
	}
	object["type"] = json.RawMessage(`"undefined"`)
	normalized, err := json.Marshal(object)
	if err != nil {
		return result
	}
	payload["result"] = normalized
	out, err := json.Marshal(payload)
	if err != nil {
		return result
	}
	return out
}
