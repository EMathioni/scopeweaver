package chatgpt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/llmpool"
	"github.com/Autumn-27/norma/llm"
)

const localTestKey = "local-key-that-is-not-an-openai-token"
const completedText = `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":12,"output_tokens":1}}}`

func bridgeFixture(t *testing.T, upstream http.HandlerFunc) (*oauthFixture, *httptest.Server) {
	t.Helper()
	f := newOAuthFixture(t)
	f.signedIn(t)
	u := httptest.NewServer(upstream)
	t.Cleanup(u.Close)
	b, err := NewBridge(f.client, localTestKey)
	if err != nil {
		t.Fatal(err)
	}
	b.upstream = u.URL
	s := httptest.NewServer(b)
	t.Cleanup(s.Close)
	return f, s
}

func streamFrames(w http.ResponseWriter, frames ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, frame := range frames {
		fmt.Fprintf(w, "data: %s\n\n", frame)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func providerForBridge(t *testing.T, endpoint string) llm.Provider {
	t.Helper()
	cfg := agent.ConfigFrom("openai-responses", "account-model", endpoint+"/v1", localTestKey, "")
	cfg.Retry.ConnectAttempts = -1
	p, err := cfg.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRealScopeWeaverProviderToolRoundTrip(t *testing.T) {
	var calls atomic.Int32
	_, s := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer access-one" {
			t.Error("wrong upstream request or credential")
		}
		var b map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&b)
		if string(b["stream"]) != "true" || string(b["store"]) != "false" || b["max_output_tokens"] != nil || b["temperature"] != nil || string(b["instructions"]) != `"host instructions"` {
			t.Error("SIWC request adaptation failed")
		}
		var tools []struct {
			Type, Name string
			Tools      []struct{ Name string }
		}
		json.Unmarshal(b["tools"], &tools)
		if len(tools) != 1 || tools[0].Type != "namespace" || tools[0].Name != toolNamespace || len(tools[0].Tools) != 1 || tools[0].Tools[0].Name != "lab_info" {
			t.Error("tool names changed")
		}
		if calls.Add(1) == 1 {
			streamFrames(w, `{"type":"response.created"}`, `{"type":"response.output_item.added","item":{"type":"function_call","namespace":"scopeweaver","call_id":"call-1","name":"lab_info"}}`, `{"type":"response.function_call_arguments.delta","delta":"{\"id\":1}"}`, `{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","namespace":"scopeweaver","call_id":"call-1","name":"lab_info","arguments":"{\"id\":1}"}],"usage":{"input_tokens":12,"output_tokens":4}}}`)
		} else {
			var input []map[string]any
			json.Unmarshal(b["input"], &input)
			if len(input) != 3 || input[1]["namespace"] != toolNamespace || input[1]["call_id"] != "call-1" || input[2]["namespace"] != toolNamespace || input[2]["name"] != "lab_info" || input[2]["output"] != "isolated lab" {
				t.Error("tool history round-trip failed")
			}
			streamFrames(w, `{"type":"response.output_text.delta","delta":"OK"}`, completedText)
		}
	})
	p := providerForBridge(t, s.URL)
	temp := 0.2
	req := llm.CompletionRequest{System: []string{"host instructions"}, Messages: []llm.Message{llm.UserText("hello")}, Tools: []llm.ToolSchema{{Name: "lab_info", InputSchema: map[string]any{"type": "object"}}}, MaxTokens: 200, Temperature: &temp}
	// Complete is used by connection tests and compaction. It must consume an
	// upstream stream atomically rather than sending forbidden stream:false.
	msg, _, usage, err := p.Complete(context.Background(), req)
	if err != nil || len(msg.ToolUses()) != 1 || msg.ToolUses()[0].ID != "call-1" || string(msg.ToolUses()[0].Input) != `{"id":1}` || usage.InputTokens != 12 {
		t.Fatal("function response lost", msg, err)
	}
	req.Messages = append(req.Messages, msg, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("call-1", "isolated lab", false)}})
	var text string
	terminal := false
	for ev, e := range p.Stream(context.Background(), req) {
		if e != nil {
			t.Fatal(e)
		}
		if ev.Type == llm.SETextDelta {
			text += ev.Text
		}
		if ev.Type == llm.SEMessageDelta {
			terminal = true
		}
	}
	if text != "OK" || !terminal || calls.Load() != 2 {
		t.Fatal("streaming turn failed")
	}
}

func TestBridgeRequiresLocalKeyAndRejectsBrowserOrigins(t *testing.T) {
	var upstream atomic.Int32
	_, s := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) { upstream.Add(1); streamFrames(w, completedText) })
	for _, tc := range []struct {
		key, host, origin string
		status            int
	}{
		{"", "", "", 401}, {"Bearer access-one", "", "", 401}, {"Bearer " + localTestKey, "attacker.invalid", "", 403}, {"Bearer " + localTestKey, "", "https://attacker.invalid", 403}, {"Bearer " + localTestKey, "", "null", 403},
	} {
		r, _ := http.NewRequest("GET", s.URL+"/health", nil)
		r.Header.Set("Authorization", tc.key)
		r.Header.Set("Origin", tc.origin)
		if tc.host != "" {
			r.Host = tc.host
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatal("local boundary failed", resp.StatusCode, tc.status)
		}
	}
	if upstream.Load() != 0 {
		t.Fatal("unauthorized request reached OpenAI")
	}
	for _, addr := range []string{"0.0.0.0:0", "[::]:0", "localhost:0", "192.0.2.1:0"} {
		if ln, err := ListenLoopback(addr); err == nil {
			ln.Close()
			t.Fatal("unsafe listener accepted")
		}
	}
}

func TestAccountModelCatalogAndHTTP401Refresh(t *testing.T) {
	var calls atomic.Int32
	f, s := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer access-one" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"code":"invalid_token"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer access-two" || r.URL.Path != "/models" {
			t.Error("renewed token/catalog endpoint wrong")
		}
		io.WriteString(w, `{"models":[{"slug":"account-model","display_name":"Account model","visibility":"list"},{"slug":"hidden","visibility":"hidden"}]}`)
	})
	r, _ := http.NewRequest("GET", s.URL+"/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+localTestKey)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"id":"account-model"`) || strings.Contains(string(raw), "hidden") || f.refreshes.Load() != 1 || calls.Load() != 2 {
		t.Fatal("catalog or 401 recovery failed", string(raw))
	}
}

func TestBridgeFailuresPreservePoolSafeWindow(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			_, s := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				frames := []string{`{"type":"response.created"}`}
				if partial {
					frames = append(frames, `{"type":"response.output_text.delta","delta":"partial"}`)
				}
				frames = append(frames, `{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"secret must not leak"}}}`)
				streamFrames(w, frames...)
			})
			var fallback atomic.Int32
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallback.Add(1)
				streamFrames(w, `{"type":"response.output_text.delta","delta":"backup"}`, completedText)
			}))
			defer backup.Close()
			pool := llmpool.New([]*llmpool.Member{{ID: 1, Prov: providerForBridge(t, s.URL), Rank: 2}, {ID: 2, Prov: providerForBridge(t, backup.URL), Rank: 1}}, nil)
			var out string
			var failure error
			for ev, err := range pool.Stream(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}}) {
				if err != nil {
					failure = err
				}
				if ev.Type == llm.SETextDelta {
					out += ev.Text
				}
			}
			if partial {
				if failure == nil || out != "partial" || fallback.Load() != 0 || strings.Contains(failure.Error(), "secret") {
					t.Fatal("mid-stream failure retried or leaked", out, failure)
				}
			} else if failure != nil || out != "backup" || fallback.Load() != 1 {
				t.Fatal("safe failover failed", out, failure)
			}
		})
	}
}

func TestIncompleteOrTruncatedStreamsNeverReportSuccess(t *testing.T) {
	for _, frame := range []string{`{"type":"response.output_text.delta","delta":"partial"}`, `{"type":"response.incomplete","response":{"status":"incomplete"}}`, `{"type":"response.completed","response":{"status":"failed"}}`, `not-json`} {
		_, s := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) { streamFrames(w, frame) })
		p := providerForBridge(t, s.URL)
		for _, stream := range []bool{false, true} {
			failed := false
			if stream {
				for _, err := range p.Stream(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}}) {
					if err != nil {
						failed = true
					}
				}
			} else {
				_, _, _, err := p.Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}})
				failed = err != nil
			}
			if !failed {
				t.Fatal("invalid stream reported success", frame, stream)
			}
		}
	}
}

func TestUnsupportedRequestsDoNotReachOpenAI(t *testing.T) {
	var requests atomic.Int32
	_, s := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); streamFrames(w, completedText) })
	for _, body := range []string{
		`{"model":"m","input":[],"previous_response_id":"r"}`,
		`{"model":"m","input":[],"tools":[{"type":"tool_search"}]}`,
		`{"model":"m","input":[{"type":"message","role":"system","content":"s"}]}`,
		`{"model":"m","input":"string"}`,
		`{"model":"m","input":[],"stream":"yes"}`,
	} {
		r, _ := http.NewRequest("POST", s.URL+"/v1/responses", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+localTestKey)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatal("unsupported request accepted", resp.StatusCode)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("unsupported request reached OpenAI")
	}
}

func TestPlanEndpointMayOmitEventStreamContentType(t *testing.T) {
	_, s := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		// Explicit nil suppresses net/http's automatic Content-Type detection.
		w.Header()["Content-Type"] = nil
		fmt.Fprintf(w, "data: %s\n\n", completedText)
	})
	p := providerForBridge(t, s.URL)
	msg, _, _, err := p.Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}})
	if err != nil || msg.Text() != "OK" {
		t.Fatalf("valid SSE without Content-Type rejected: %v", err)
	}
}

func TestLeanCompletedResponseReconstructsFinalOutput(t *testing.T) {
	_, s := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		streamFrames(w,
			`{"type":"response.output_text.delta","delta":"OK"}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call-1","name":"lab_info","arguments":"{}","namespace":"scopeweaver"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}}`,
			`{"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":12,"output_tokens":4}}}`)
	})
	p := providerForBridge(t, s.URL)
	msg, _, usage, err := p.Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("test")}})
	if err != nil || msg.Text() != "OK" || len(msg.ToolUses()) != 1 || msg.ToolUses()[0].ID != "call-1" || usage.OutputTokens != 4 || msg.Content[0].Type != llm.BlockText {
		t.Fatalf("lean final response lost output/order/usage: %+v %v", msg, err)
	}
}
