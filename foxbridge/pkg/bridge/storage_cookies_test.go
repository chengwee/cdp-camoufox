package bridge

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/chengwee/cdp-camoufox/foxbridge/pkg/cdp"
)

func TestHandleStorage(t *testing.T) {
	tests := []struct {
		method        string
		params        string
		backendMethod string
		backendParams string
	}{
		{"Storage.getCookies", `{}`, "Browser.getCookies", `{}`},
		{"Storage.clearCookies", `{"browserContextId":"context"}`, "Browser.clearCookies", `{"browserContextId":"context"}`},
		{"Storage.setCookies", `{"cookies":[{"name":"n","value":"v","url":"https://example.com","domain":"example.com","path":"/","secure":true,"httpOnly":true,"sameSite":"Lax","expires":123,"priority":"High"}]}`, "Browser.setCookies", `{"cookies":[{"name":"n","value":"v","url":"https://example.com","domain":"example.com","path":"/","secure":true,"httpOnly":true,"sameSite":"Lax","expires":123}]}`},
		{"Storage.getCookies", `{"browserContextId":"` + syntheticDefaultBrowserContextID + `"}`, "Browser.getCookies", `{}`},
	}
	for _, test := range tests {
		t.Run(test.method+test.params, func(t *testing.T) {
			bridge, backend := newTestBridge()
			backend.SetResponse("", test.backendMethod, json.RawMessage(`{"cookies":[]}`), nil)
			result, protocolErr := bridge.handleStorage(nil, &cdp.Message{Method: test.method, Params: json.RawMessage(test.params)})
			if protocolErr != nil {
				t.Fatal(protocolErr)
			}
			call, err := backend.LastCall()
			if err != nil {
				t.Fatal(err)
			}
			var actual, expected interface{}
			json.Unmarshal(call.Params, &actual)
			json.Unmarshal([]byte(test.backendParams), &expected)
			if call.SessionID != "" || call.Method != test.backendMethod || !reflect.DeepEqual(actual, expected) {
				t.Fatalf("unexpected call: %+v", call)
			}
			if test.method == "Storage.getCookies" && string(result) != `{"cookies":[]}` {
				t.Fatalf("unexpected result: %s", result)
			}
		})
	}
}

func TestHandleStorageErrors(t *testing.T) {
	for _, method := range []string{"Storage.getCookies", "Storage.setCookies", "Storage.clearCookies"} {
		t.Run(method, func(t *testing.T) {
			bridge, backend := newTestBridge()
			backend.SetResponse("", "Browser."+method[len("Storage."):], nil, errors.New("backend failed"))
			params := `{}`
			if method == "Storage.setCookies" {
				params = `{"cookies":[]}`
			}
			_, protocolErr := bridge.handleStorage(nil, &cdp.Message{Method: method, Params: json.RawMessage(params)})
			if protocolErr == nil || protocolErr.Message != "backend failed" {
				t.Fatalf("unexpected error: %v", protocolErr)
			}
		})
	}
	for _, params := range []string{`{}`, `{"cookies":null}`, `{"cookies":[{}]}`, `{"cookies":[{"name":"n","value":"v","partitionKey":{"topLevelSite":"https://example.com"}}]}`} {
		bridge, backend := newTestBridge()
		_, protocolErr := bridge.handleStorage(nil, &cdp.Message{Method: "Storage.setCookies", Params: json.RawMessage(params)})
		if protocolErr == nil || backend.CallCount() != 0 {
			t.Fatalf("expected rejection for %s, got %v", params, protocolErr)
		}
	}
}
