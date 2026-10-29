package bridge

import (
	"encoding/json"
	"fmt"

	"github.com/chengwee/cdp-camoufox/foxbridge/pkg/cdp"
)

func (b *Bridge) handleStorage(conn *cdp.Connection, msg *cdp.Message) (json.RawMessage, *cdp.Error) {
	var backendMethod string
	switch msg.Method {
	case "Storage.getCookies":
		backendMethod = "Browser.getCookies"
	case "Storage.setCookies":
		backendMethod = "Browser.setCookies"
	case "Storage.clearCookies":
		backendMethod = "Browser.clearCookies"
	default:
		return nil, &cdp.Error{Code: -32601, Message: fmt.Sprintf("method not found: %s", msg.Method)}
	}

	var params struct {
		BrowserContextID string            `json:"browserContextId"`
		Cookies          []json.RawMessage `json:"cookies"`
	}
	if len(msg.Params) > 0 && string(msg.Params) != "null" {
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
		}
	}
	backendParams := map[string]interface{}{}
	b.setJugglerBrowserContext(backendParams, params.BrowserContextID)
	if msg.Method == "Storage.setCookies" {
		if params.Cookies == nil {
			return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
		}
		cookies := make([]map[string]json.RawMessage, 0, len(params.Cookies))
		for _, rawCookie := range params.Cookies {
			var cookie map[string]json.RawMessage
			if err := json.Unmarshal(rawCookie, &cookie); err != nil || cookie == nil {
				return nil, &cdp.Error{Code: -32602, Message: "invalid cookie"}
			}
			if _, exists := cookie["partitionKey"]; exists {
				return nil, &cdp.Error{Code: -32602, Message: "partitionKey is not supported"}
			}
			var name, value string
			if len(cookie["name"]) == 0 || len(cookie["value"]) == 0 || string(cookie["name"]) == "null" || string(cookie["value"]) == "null" || json.Unmarshal(cookie["name"], &name) != nil || json.Unmarshal(cookie["value"], &value) != nil {
				return nil, &cdp.Error{Code: -32602, Message: "invalid cookie"}
			}
			accepted := map[string]json.RawMessage{}
			for _, field := range []string{"name", "value", "url", "domain", "path", "secure", "httpOnly", "sameSite", "expires"} {
				if value, exists := cookie[field]; exists {
					accepted[field] = value
				}
			}
			cookies = append(cookies, accepted)
		}
		backendParams["cookies"] = cookies
	}
	result, err := b.callJuggler("", backendMethod, backendParams)
	if err != nil {
		return nil, &cdp.Error{Code: -32000, Message: err.Error()}
	}
	if msg.Method == "Storage.getCookies" {
		return result, nil
	}
	return json.RawMessage(`{}`), nil
}
