package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/store"
)

// The project's gateway as OpenAI's API: a member's token, the project's key
// to the gateway, the caller as the request's user whatever it says, the
// answer (or the gateway's refusal) passed back as it came, streamed too.
func TestTheProjectsGatewayAsOpenAIsAPI(t *testing.T) {
	var mu sync.Mutex
	var saw []map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-sales" {
			http.Error(w, `{"error":{"message":"invalid key"}}`, 401)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/models" {
			w.Write([]byte(`{"object":"list","data":[{"id":"default","object":"model"},{"id":"embedding","object":"model"}]}`))
			return
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		saw = append(saw, b)
		mu.Unlock()
		if strings.Contains(r.URL.Path, "/chat/completions") && strings.Contains(b["messages"].([]any)[0].(map[string]any)["content"].(string), "4111") {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"a card number may not leave the platform","type":"guardrail","code":"400"}}`))
			return
		}
		if b["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n"))
			w.(http.Flusher).Flush()
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-litellm-response-cost", "0.0042")
		w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"default","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	defer gw.Close()
	sales, _ := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"alice","role":"reader"}],"tables":{},"llm":{"budget":5}}}`))
	keyless, _ := project.Parse([]byte(`{"metadata":{"name":"keyless"},"spec":{"members":[{"user":"alice","role":"reader"}],"tables":{}}}`))
	members := []project.Person{{Username: "alice", Role: "reader"}}
	st := &connsKept{projectsKept: projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Members: members, Spec: sales}, {Name: "keyless", Members: members, Spec: keyless}}},
		specs: map[string]*project.Project{"sales": sales, "keyless": keyless}},
		conns: map[string]store.StoredConnection{"sales/sales-llm": {ConnectionMeta: store.ConnectionMeta{Name: "sales-llm"}, Data: map[string]string{"llmKey": "sk-sales", "llmUrl": gw.URL}}}}
	s := &server{cfg: config{Groups: []string{"analysts"}}, store: st, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		tokenUser: func(raw string) (string, error) { return raw, nil }, sessions: map[string]*session{}}
	h := s.routes()
	call := func(who, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+who)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	base := "/api/projects/sales/llm/v1"

	w := call("alice", "POST", base+"/chat/completions", `{"messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"hi"}],"user":"someone-else"}`)
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || len(out.Choices) != 1 || out.Choices[0].Message.Content != "Hello" || w.Header().Get("x-litellm-response-cost") != "0.0042" {
		t.Fatalf("a completion: %d %s", w.Code, w.Body.String())
	}
	if saw[0]["user"] != "alice" || saw[0]["model"] != "default" || len(saw[0]["messages"].([]any)) != 2 {
		t.Errorf("the gateway was asked %v: want alice as the user, model default, both messages", saw[0])
	}

	w = call("alice", "POST", base+"/chat/completions", `{"model":"default","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Hel"`) || !strings.HasSuffix(w.Body.String(), "data: [DONE]\n\n") || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("a stream: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}

	w = call("alice", "POST", base+"/embeddings", `{"input":["a","b"]}`)
	if saw[len(saw)-1]["model"] != "embedding" || saw[len(saw)-1]["user"] != "alice" {
		t.Errorf("embeddings: the gateway was asked %v", saw[len(saw)-1])
	}
	if w = call("alice", "GET", base+"/models", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"default"`) {
		t.Errorf("models: %d %s", w.Code, w.Body.String())
	}

	if w = call("alice", "POST", base+"/chat/completions", `{"messages":[{"role":"user","content":"card 4111 1111 1111 1111"}]}`); w.Code != 400 || !strings.Contains(w.Body.String(), "may not leave") {
		t.Errorf("the guardrail's refusal, passed back: %d %s", w.Code, w.Body.String())
	}
	if w = call("carol", "POST", base+"/chat/completions", `{"messages":[]}`); w.Code != 403 {
		t.Errorf("a non-member: %d %s", w.Code, w.Body.String())
	}
	w = call("alice", "POST", "/api/projects/keyless/llm/v1/chat/completions", `{"messages":[]}`)
	var e struct {
		Error struct{ Message string } `json:"error"`
	}
	json.Unmarshal(w.Body.Bytes(), &e)
	if w.Code != 422 || !strings.Contains(e.Error.Message, "LLM budget") {
		t.Errorf("a project without a key, as OpenAI's error: %d %s", w.Code, w.Body.String())
	}
	if w = call("alice", "POST", base+"/completions", `{}`); w.Code != 404 {
		t.Errorf("a route the gateway isn't asked: %d", w.Code)
	}
	if w = call("alice", "POST", base+"/chat/completions", `not json`); w.Code != 400 {
		t.Errorf("not JSON: %d %s", w.Code, w.Body.String())
	}
}
