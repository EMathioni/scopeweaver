package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/artex/locale"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxBody = 16 << 20
const toolNamespace = "scopeweaver"

type Bridge struct {
	client   *Client
	key      string
	upstream string
	http     *http.Client
}

func NewBridge(client *Client, localKey string) (*Bridge, error) {
	if len(localKey) < 32 {
		return nil, locale.NewError("local bridge key is required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 90 * time.Second
	return &Bridge{client: client, key: localKey, upstream: resource,
		http: &http.Client{Transport: transport, CheckRedirect: noRedirect}}, nil
}

// ListenLoopback deliberately forbids wildcard and hostname listeners. A local
// bridge grants access to a user's plan; it must not become a public gateway.
func ListenLoopback(address string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, locale.NewError("bridge must listen on a literal loopback IP address")
	}
	return net.Listen("tcp", address)
}

func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if (host != "localhost" && (ip == nil || !ip.IsLoopback())) || r.Header.Get("Origin") != "" {
		bridgeError(w, 403, "local_clients_only")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+b.key)) != 1 {
		bridgeError(w, 401, "invalid_bridge_key")
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/health":
		_, _, signedIn := b.client.Account()
		writeBridgeJSON(w, 200, map[string]any{"ok": signedIn, "provider": "chatgpt"})
	case r.Method == "GET" && r.URL.Path == "/v1/models":
		b.models(w, r)
	case r.Method == "POST" && r.URL.Path == "/v1/responses":
		b.responses(w, r)
	default:
		bridgeError(w, 404, "unsupported_bridge_endpoint")
	}
}

func writeBridgeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func bridgeError(w http.ResponseWriter, status int, code string) {
	writeBridgeJSON(w, status, map[string]any{"error": map[string]any{
		"code": code, "type": "chatgpt_bridge_error", "message": fmt.Sprintf("ChatGPT bridge: status %d %s", status, code),
	}})
}

func (b *Bridge) request(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	token, err := b.client.Token(ctx)
	if err != nil {
		return nil, err
	}
	send := func(access string) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, method, b.upstream+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+access)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("User-Agent", "ScopeWeaver-ChatGPT-Bridge/1")
		if method == "POST" {
			r.Header.Set("Accept", "text/event-stream")
		}
		return b.http.Do(r)
	}
	resp, err := send(token)
	if err != nil {
		return nil, locale.NewError("OpenAI inference endpoint unavailable")
	}
	// Retry only an HTTP 401, before reading/yielding any inference content.
	if resp.StatusCode == 401 {
		resp.Body.Close()
		token, err = b.client.RefreshAfterUnauthorized(ctx, token)
		if err != nil {
			return nil, err
		}
		resp, err = send(token)
		if err != nil {
			return nil, locale.NewError("OpenAI inference endpoint unavailable")
		}
	}
	return resp, nil
}

func (b *Bridge) models(w http.ResponseWriter, r *http.Request) {
	resp, err := b.request(r.Context(), "GET", "/models", nil)
	if err != nil {
		bridgeError(w, requestErrorStatus(err), "chatgpt_unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		forwardHTTPError(w, resp)
		return
	}
	var catalog struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&catalog) != nil || catalog.Models == nil {
		bridgeError(w, 502, "invalid_model_catalog")
		return
	}
	data := make([]map[string]any, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		if m.Visibility == "list" && m.Slug != "" {
			data = append(data, map[string]any{"id": m.Slug, "object": "model", "display_name": m.DisplayName, "owned_by": "openai"})
		}
	}
	writeBridgeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func requestErrorStatus(err error) int {
	var oauth *OAuthError
	if errors.As(err, &oauth) {
		if oauth.Code == "invalid_grant" || oauth.Code == "invalid_client" {
			return 401
		}
		return 503
	}
	if err.Error() == "ChatGPT sign-in required" {
		return 401
	}
	return 503
}

// adaptRequest preserves the host's messages and function names. The SIWC
// preview requires namespaced tools and full history, and forbids output caps,
// temperature and server-stored state. The regular API provider is unaffected.
func adaptRequest(raw []byte) ([]byte, bool, error) {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return nil, false, locale.NewError("invalid Responses request")
	}
	var model string
	if json.Unmarshal(body["model"], &model) != nil || strings.TrimSpace(model) == "" {
		return nil, false, locale.NewError("model is required")
	}
	stream := false
	if v, ok := body["stream"]; ok && json.Unmarshal(v, &stream) != nil {
		return nil, false, locale.NewError("invalid streaming option")
	}
	// Allow only the stateless subset used by ScopeWeaver/Norma. Do not turn an
	// unrelated API request into a subtly different call by dropping its state.
	for k := range body {
		switch k {
		case "model", "instructions", "input", "tools", "reasoning", "stream", "store", "max_output_tokens", "temperature":
		default:
			return nil, false, fmt.Errorf("unsupported Responses field: %s", k)
		}
	}
	var input []map[string]json.RawMessage
	if json.Unmarshal(body["input"], &input) != nil {
		return nil, false, locale.NewError("input must be an array")
	}
	if input == nil {
		input = []map[string]json.RawMessage{}
	}
	calls := map[string]string{}
	for _, item := range input {
		var kind, role, id, name string
		_ = json.Unmarshal(item["type"], &kind)
		_ = json.Unmarshal(item["role"], &role)
		if role == "system" {
			return nil, false, locale.NewError("use instructions or developer messages")
		}
		if kind == "function_call" {
			_ = json.Unmarshal(item["call_id"], &id)
			_ = json.Unmarshal(item["name"], &name)
			item["namespace"] = json.RawMessage(`"` + toolNamespace + `"`)
			calls[id] = name
		}
		if kind == "function_call_output" {
			_ = json.Unmarshal(item["call_id"], &id)
			if name := calls[id]; name != "" {
				item["name"], _ = json.Marshal(name)
				item["namespace"] = json.RawMessage(`"` + toolNamespace + `"`)
			}
		}
	}
	body["input"], _ = json.Marshal(input)
	if v, ok := body["tools"]; ok {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(v, &tools) != nil {
			return nil, false, locale.NewError("invalid tools array")
		}
		for _, tool := range tools {
			var kind string
			_ = json.Unmarshal(tool["type"], &kind)
			if kind != "function" {
				return nil, false, locale.NewError("bridge supports local function tools only")
			}
			delete(tool, "defer_loading") // SIWC does not support tool_search
		}
		if len(tools) > 0 {
			body["tools"], _ = json.Marshal([]any{map[string]any{"type": "namespace", "name": toolNamespace, "description": "Tools supplied and executed by ScopeWeaver in its configured task scope.", "tools": tools}})
		} else {
			delete(body, "tools")
		}
	}
	delete(body, "max_output_tokens")
	delete(body, "temperature")
	body["store"], body["stream"] = json.RawMessage("false"), json.RawMessage("true")
	b, err := json.Marshal(body)
	return b, stream, err
}

var errorCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

func safeCode(code string) string {
	if errorCode.MatchString(code) {
		return code
	}
	return "upstream_error"
}

func failureCode(raw []byte) string {
	var frame struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Response struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return "upstream_error"
	}
	if frame.Response.Error.Code != "" {
		return safeCode(frame.Response.Error.Code)
	}
	return safeCode(frame.Error.Code)
}

func failureStatus(code string) int {
	switch code {
	case "subscription_sharing_usage_limit_exceeded", "rate_limit_exceeded":
		return 429
	case "subscription_sharing_usage_unavailable":
		return 403
	case "insufficient_quota":
		return 402
	case "invalid_api_key", "invalid_token", "authentication_error":
		return 401
	default:
		return 502
	}
}

func forwardHTTPError(w http.ResponseWriter, resp *http.Response) {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	status := resp.StatusCode
	if status < 400 || status > 599 {
		status = 502
	}
	if retry := resp.Header.Get("Retry-After"); retry != "" {
		w.Header().Set("Retry-After", retry)
	}
	bridgeError(w, status, failureCode(raw))
}

func (b *Bridge) responses(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		bridgeError(w, 413, "request_too_large")
		return
	}
	body, stream, err := adaptRequest(raw)
	if err != nil {
		bridgeError(w, 400, "invalid_chatgpt_request")
		return
	}
	resp, err := b.request(r.Context(), "POST", "/responses", body)
	if err != nil {
		bridgeError(w, requestErrorStatus(err), "chatgpt_unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		forwardHTTPError(w, resp)
		return
	}
	// The plan-usage endpoint may omit Content-Type even when returning valid
	// SSE. The parser below still requires valid events and response.completed.
	if contentType := resp.Header.Get("Content-Type"); contentType != "" && !strings.HasPrefix(contentType, "text/event-stream") {
		bridgeError(w, 502, "expected_event_stream")
		return
	}
	scan := bufio.NewScanner(resp.Body)
	scan.Buffer(make([]byte, 4096), maxBody)
	started, completed := false, false
	emit := func(event string, data []byte) bool {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			started = true
		}
		if _, e := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); e != nil {
			return false
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return true
	}
	fail := func(code string) {
		status := failureStatus(code)
		if !started {
			bridgeError(w, status, code)
			return
		}
		data, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"code": code, "message": fmt.Sprintf("ChatGPT bridge: status %d %s", status, code)}})
		emit("error", data)
	}
	var event string
	var data []string
	// Plan-usage streams may leave response.completed.output empty. Preserve
	// finalized items for atomic callers (connection tests and compaction).
	items := make(map[int]json.RawMessage)
	itemBytes := 0
	process := func() bool {
		if len(data) == 0 {
			event = ""
			return true
		}
		payload := []byte(strings.Join(data, "\n"))
		var frame struct {
			Type        string          `json:"type"`
			Response    json.RawMessage `json:"response"`
			Item        json.RawMessage `json:"item"`
			OutputIndex int             `json:"output_index"`
		}
		if json.Unmarshal(payload, &frame) != nil {
			fail("invalid_event_stream")
			return false
		}
		kind := frame.Type
		if kind == "" {
			kind = event
		}
		if kind == "response.failed" || kind == "response.incomplete" || kind == "error" {
			code := failureCode(payload)
			if kind == "response.incomplete" {
				code = "incomplete_response"
			}
			fail(code)
			return false
		}
		if kind == "response.completed" {
			var final struct {
				Status string            `json:"status"`
				Output []json.RawMessage `json:"output"`
			}
			if json.Unmarshal(frame.Response, &final) != nil || final.Status != "completed" {
				fail("invalid_completed_response")
				return false
			}
			if stream {
				if !emit(kind, payload) {
					return false
				}
			} else {
				if len(final.Output) == 0 && len(items) > 0 {
					indexes := make([]int, 0, len(items))
					for index := range items {
						indexes = append(indexes, index)
					}
					sort.Ints(indexes)
					for _, index := range indexes {
						final.Output = append(final.Output, items[index])
					}
					var response map[string]json.RawMessage
					_ = json.Unmarshal(frame.Response, &response)
					response["output"], _ = json.Marshal(final.Output)
					writeBridgeJSON(w, 200, response)
				} else {
					writeBridgeJSON(w, 200, json.RawMessage(frame.Response))
				}
			}
			completed = true
			return false
		}
		if kind == "response.output_item.done" && !stream {
			if len(frame.Item) == 0 || string(frame.Item) == "null" || frame.OutputIndex < 0 || frame.OutputIndex > 10000 {
				fail("invalid_output_item")
				return false
			}
			itemBytes += len(frame.Item) - len(items[frame.OutputIndex])
			if itemBytes > maxBody {
				fail("response_too_large")
				return false
			}
			items[frame.OutputIndex] = frame.Item
		}
		// Only events the existing provider understands are forwarded. Metadata
		// events do not commit HTTP 200 before a quota/auth error is known.
		switch kind {
		case "response.output_text.delta", "response.output_item.added", "response.function_call_arguments.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if stream && !emit(kind, payload) {
				return false
			}
		}
		event = ""
		data = nil
		return true
	}
	for scan.Scan() {
		line := scan.Text()
		if line == "" {
			if !process() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			if len(strings.Join(data, "\n")) > maxBody {
				fail("event_too_large")
				return
			}
		}
	}
	if len(data) > 0 {
		if !process() {
			return
		}
	}
	if !completed {
		fail("interrupted_event_stream")
	}
}
