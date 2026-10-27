package bridge

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/VulpineOS/foxbridge/pkg/cdp"
)

func (b *Bridge) handleNetwork(conn *cdp.Connection, msg *cdp.Message) (json.RawMessage, *cdp.Error) {
	if conn != nil {
		switch msg.Method {
		case "Network.setRequestInterception", "Network.setUserAgentOverride", "Network.setExtraHTTPHeaders":
			return nil, &cdp.Error{Code: -32000, Message: "shared browser-global setting is disabled"}
		}
	}
	switch msg.Method {
	case "Network.enable", "Network.disable":
		// Juggler auto-enables network events; no-op.
		return json.RawMessage(`{}`), nil

	case "Network.setCacheDisabled":
		return json.RawMessage(`{}`), nil

	case "Network.setCookies":
		var params struct {
			Cookies []json.RawMessage `json:"cookies"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
		}

		jugglerParams := map[string]interface{}{
			"cookies": params.Cookies,
		}

		// Include browserContextId if we have a session with one.
		if msg.SessionID != "" {
			if info, ok := b.sessions.Get(msg.SessionID); ok {
				b.setJugglerBrowserContext(jugglerParams, info.BrowserContextID)
			}
		}

		_, err := b.callJuggler("", "Browser.setCookies", jugglerParams)
		if err != nil {
			return nil, &cdp.Error{Code: -32000, Message: err.Error()}
		}
		return json.RawMessage(`{}`), nil

	case "Network.getCookies":
		var params struct {
			URLs []string `json:"urls"`
		}
		if msg.Params != nil {
			json.Unmarshal(msg.Params, &params)
		}

		// Juggler's getCookies only accepts browserContextId — no URLs filter
		jugglerParams := map[string]interface{}{}
		if msg.SessionID != "" {
			if info, ok := b.sessions.Get(msg.SessionID); ok {
				b.setJugglerBrowserContext(jugglerParams, info.BrowserContextID)
			}
		}

		result, err := b.callJuggler("", "Browser.getCookies", jugglerParams)
		if err != nil {
			return nil, &cdp.Error{Code: -32000, Message: err.Error()}
		}
		return result, nil

	case "Network.deleteCookies":
		// Puppeteer uses this to delete specific cookies
		// Map to clearBrowserCookies as fallback (clears all for context)
		jugglerParams := map[string]interface{}{}
		if msg.SessionID != "" {
			if info, ok := b.sessions.Get(msg.SessionID); ok {
				b.setJugglerBrowserContext(jugglerParams, info.BrowserContextID)
			}
		}
		b.callJuggler("", "Browser.clearCookies", jugglerParams)
		return json.RawMessage(`{}`), nil

	case "Network.clearBrowserCookies":
		jugglerParams := map[string]interface{}{}
		if msg.SessionID != "" {
			if info, ok := b.sessions.Get(msg.SessionID); ok {
				b.setJugglerBrowserContext(jugglerParams, info.BrowserContextID)
			}
		}

		_, err := b.callJuggler("", "Browser.clearCookies", jugglerParams)
		if err != nil {
			return nil, &cdp.Error{Code: -32000, Message: err.Error()}
		}
		return json.RawMessage(`{}`), nil

	case "Network.setExtraHTTPHeaders":
		var params struct {
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
		}

		jugglerParams := map[string]interface{}{
			"headers": params.Headers,
		}
		if msg.SessionID != "" {
			if info, ok := b.sessions.Get(msg.SessionID); ok {
				b.setJugglerBrowserContext(jugglerParams, info.BrowserContextID)
			}
		}

		_, err := b.callJuggler("", "Browser.setExtraHTTPHeaders", jugglerParams)
		if err != nil {
			return nil, &cdp.Error{Code: -32000, Message: err.Error()}
		}
		return json.RawMessage(`{}`), nil

	case "Network.setRequestInterception":
		var params struct {
			Patterns []json.RawMessage `json:"patterns"`
		}
		if msg.Params != nil {
			json.Unmarshal(msg.Params, &params)
		}

		jugglerParams := map[string]interface{}{
			"enabled": len(params.Patterns) > 0,
		}
		if msg.SessionID != "" {
			if info, ok := b.sessions.Get(msg.SessionID); ok {
				b.setJugglerBrowserContext(jugglerParams, info.BrowserContextID)
			}
		}

		_, err := b.callJuggler("", "Browser.setRequestInterception", jugglerParams)
		if err != nil {
			return nil, &cdp.Error{Code: -32000, Message: err.Error()}
		}
		return json.RawMessage(`{}`), nil

	case "Network.setUserAgentOverride":
		var params struct {
			UserAgent string `json:"userAgent"`
		}
		if msg.Params != nil {
			json.Unmarshal(msg.Params, &params)
		}
		if params.UserAgent != "" {
			jugglerUA := map[string]interface{}{
				"userAgent": params.UserAgent,
			}
			if msg.SessionID != "" {
				if info, ok := b.sessions.Get(msg.SessionID); ok {
					b.setJugglerBrowserContext(jugglerUA, info.BrowserContextID)
				}
			}
			b.callJuggler("", "Browser.setUserAgentOverride", jugglerUA)
		}
		return json.RawMessage(`{}`), nil

	case "Network.emulateNetworkConditions":
		// Juggler doesn't support network throttling directly.
		// No-op but return success so Puppeteer doesn't error.
		return json.RawMessage(`{}`), nil

	case "Network.getResponseBody":
		var params struct {
			RequestID string `json:"requestId"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, &cdp.Error{Code: -32602, Message: "invalid params"}
		}
		if conn != nil {
			if err := b.authorizeRequest(msg.SessionID, params.RequestID); err != nil {
				return nil, err
			}
		}

		if !b.isBiDi {
			result, err := b.callJuggler(msg.SessionID, "Network.getResponseBody", map[string]string{
				"requestId": params.RequestID,
			})
			if err != nil {
				return nil, &cdp.Error{Code: -32000, Message: err.Error()}
			}
			var response struct {
				Base64Body string `json:"base64body"`
				Evicted    bool   `json:"evicted"`
			}
			if err := json.Unmarshal(result, &response); err != nil {
				return nil, &cdp.Error{Code: -32000, Message: err.Error()}
			}
			if response.Evicted {
				b.forgetNetworkRequest(msg.SessionID, params.RequestID)
				return nil, &cdp.Error{Code: -32000, Message: "response body was evicted"}
			}
			b.networkRequestsMu.RLock()
			request := b.networkRequests[msg.SessionID+"\x00"+params.RequestID]
			b.networkRequestsMu.RUnlock()
			text := response.Base64Body
			base64Encoded := true
			if textualMimeType(request.MimeType) {
				decoded, err := base64.StdEncoding.DecodeString(response.Base64Body)
				if err != nil {
					return nil, &cdp.Error{Code: -32000, Message: "invalid base64 response body"}
				}
				if utf8.Valid(decoded) {
					text, base64Encoded = string(decoded), false
				}
			}
			body, err := json.Marshal(struct {
				Body          string `json:"body"`
				Base64Encoded bool   `json:"base64Encoded"`
			}{Body: text, Base64Encoded: base64Encoded})
			if err != nil {
				return nil, &cdp.Error{Code: -32000, Message: err.Error()}
			}
			return body, nil
		}

		result, err := b.callJuggler("", "Browser.getResponseBody", map[string]string{
			"requestId": params.RequestID,
		})
		if err != nil {
			return nil, &cdp.Error{Code: -32000, Message: err.Error()}
		}
		return result, nil

	default:
		return nil, &cdp.Error{Code: -32601, Message: fmt.Sprintf("method not found: %s", msg.Method)}
	}
}
