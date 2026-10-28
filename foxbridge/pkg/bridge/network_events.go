package bridge

import (
	"encoding/json"
	"strings"
	"time"
)

type networkRequestInfo struct {
	SessionID    string
	URL          string
	FrameID      string
	ResourceType string
	MimeType     string
	CompletedAt  uint64
}

// Keep recent bodies readable after completion without evicting in-flight metadata.
const completedRequestLimit = 1024

func (b *Bridge) finishNetworkRequest(sessionID, requestID string) {
	b.networkRequestsMu.Lock()
	defer b.networkRequestsMu.Unlock()
	key := sessionID + "\x00" + requestID
	request, ok := b.networkRequests[key]
	if !ok || request.CompletedAt != 0 {
		return
	}
	b.completedRequestSequence++
	request.CompletedAt = b.completedRequestSequence
	b.networkRequests[key] = request
	if len(b.networkRequests) <= completedRequestLimit {
		return
	}
	completed := 0
	oldestKey := ""
	var oldest uint64
	for candidateKey, candidate := range b.networkRequests {
		if candidate.CompletedAt == 0 {
			continue
		}
		completed++
		if oldestKey == "" || candidate.CompletedAt < oldest {
			oldestKey, oldest = candidateKey, candidate.CompletedAt
		}
	}
	if completed > completedRequestLimit {
		delete(b.networkRequests, oldestKey)
		_, evictedID, _ := strings.Cut(oldestKey, "\x00")
		b.ownership.clearRequest(evictedID)
	}
}

func (b *Bridge) forgetNetworkRequest(sessionID, requestID string) {
	b.networkRequestsMu.Lock()
	delete(b.networkRequests, sessionID+"\x00"+requestID)
	b.ownership.clearRequest(requestID)
	b.networkRequestsMu.Unlock()
}

func networkResourceType(cause string) string {
	switch strings.TrimPrefix(strings.ToLower(cause), "type_") {
	case "fetch":
		return "Fetch"
	case "xmlhttprequest", "xhr":
		return "XHR"
	case "document", "subdocument":
		return "Document"
	case "script":
		return "Script"
	case "stylesheet":
		return "Stylesheet"
	case "image", "imageset":
		return "Image"
	case "font":
		return "Font"
	case "media":
		return "Media"
	case "websocket":
		return "WebSocket"
	default:
		return "Other"
	}
}

func (b *Bridge) networkTimestamp() float64 { return time.Since(b.startedAt).Seconds() }

func responseMimeType(headers map[string]string) string {
	for name, value := range headers {
		if strings.EqualFold(name, "content-type") {
			mimeType, _, _ := strings.Cut(value, ";")
			return strings.TrimSpace(mimeType)
		}
	}
	return ""
}

func textualMimeType(mimeType string) bool {
	mimeType = strings.ToLower(mimeType)
	return strings.HasPrefix(mimeType, "text/") || strings.HasSuffix(mimeType, "+json") || strings.HasSuffix(mimeType, "+xml") || mimeType == "application/json" || mimeType == "application/javascript" || mimeType == "application/xml" || mimeType == "application/x-www-form-urlencoded"
}

func networkHeaders(raw json.RawMessage) map[string]string {
	headers := make(map[string]string)
	var entries []headerEntry
	if json.Unmarshal(raw, &entries) == nil {
		for _, entry := range entries {
			headers[entry.Name] = entry.Value
		}
		return headers
	}
	_ = json.Unmarshal(raw, &headers)
	return headers
}
