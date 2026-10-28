package bridge

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/VulpineOS/foxbridge/pkg/cdp"
)

func TestNetworkCompletedRequestRetention(t *testing.T) {
	b, backend := newTestBridge()
	b.sessions.Add(&cdp.SessionInfo{SessionID: "page", JugglerSessionID: "juggler", TargetID: "target"})
	b.SetupEventSubscriptions()
	emit := func(method string, params interface{}) {
		for _, handler := range backend.handlers[method] {
			handler("juggler", mustMarshal(params))
		}
	}
	start := func(id string) {
		emit("Network.requestWillBeSent", map[string]interface{}{"requestId": id, "url": "https://example.test/data", "method": "GET", "cause": "TYPE_FETCH"})
		emit("Network.responseReceived", map[string]interface{}{"requestId": id, "headers": []map[string]string{{"name": "Content-Type", "value": "application/json"}}})
	}
	start("in-flight")
	for index := 0; index < 1030; index++ {
		id := fmt.Sprintf("request-%d", index)
		start(id)
		emit("Network.requestFinished", map[string]interface{}{"requestId": id})
	}
	if len(b.networkRequests) > 1025 {
		t.Fatalf("retained %d requests, want at most 1024 completed plus one in-flight", len(b.networkRequests))
	}
	if _, ok := b.networkRequests["page\x00in-flight"]; !ok {
		t.Fatal("in-flight request was evicted")
	}
	if b.ownership.requestOwned("page", "request-0") {
		t.Fatal("evicted request still authorized")
	}
	if !b.ownership.requestOwned("page", "request-6") {
		t.Fatal("oldest retained request lost authorization")
	}
	backend.SetResponse("juggler", "Network.getResponseBody", json.RawMessage(`{"base64body":"eyJwcmljZSI6NDJ9"}`), nil)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := b.handleNetwork(nil, &cdp.Message{SessionID: "page", Method: "Network.getResponseBody", Params: json.RawMessage(`{"requestId":"request-1029"}`)})
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Body          string
			Base64Encoded bool
		}
		if err := json.Unmarshal(result, &body); err != nil {
			t.Fatal(err)
		}
		if body.Body != `{"price":42}` || body.Base64Encoded {
			t.Fatalf("delayed body = %s", result)
		}
	}
	emit("Network.requestFailed", map[string]interface{}{"requestId": "in-flight", "errorCode": "failed"})
	if _, ok := b.networkRequests["page\x00in-flight"]; ok {
		t.Fatal("failed request metadata retained")
	}
}
