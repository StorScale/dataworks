package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// The project's LLM gateway as OpenAI's API (its chat completions,
// embeddings and models), for any OpenAI client: base URL
// <platform>/api/projects/<name>/llm/v1, the API key a DataWorks token of a
// member's (a person's, the project's service account's, or a person's got
// on their behalf by it). Each request goes to the gateway on the project's
// key, so its budget and guardrail hold, with the gateway's answer passed back
// as it came, streamed when asked (stream: true). The request's user is
// always the caller, whatever it says: the spend is theirs on the project's
// Model calls, and the audit log has each call (llm.asked) with its cost.
// The question and the answer are never kept.

// llmProxyRoutes are the OpenAI routes passed to the gateway, by method.
var llmProxyRoutes = map[string]string{
	"POST chat/completions": "/chat/completions",
	"POST embeddings":       "/embeddings",
	"GET models":            "/models",
}

func (s *server) handleLLMProxy(w http.ResponseWriter, r *http.Request) {
	upstream, ok := llmProxyRoutes[r.Method+" "+r.PathValue("op")]
	if !ok {
		openAIError(w, http.StatusNotFound, "not_found", "the project's gateway answers chat/completions, embeddings and models")
		return
	}
	v, who := s.agentProject(w, r)
	if v == nil {
		return
	}
	conn, err := s.store.Connection(r.Context(), v.Name, v.Name+"-llm")
	if err != nil || conn["llmKey"] == "" || conn["llmUrl"] == "" {
		openAIError(w, http.StatusUnprocessableEntity, "no_key",
			"the project has no gateway key: give it an LLM budget (its Edit page, or llm.budget in its spec) and the operator makes one")
		return
	}
	var body []byte
	model, stream := "", false
	if r.Method == http.MethodPost {
		var req map[string]any
		dec := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
		dec.UseNumber()
		if err := dec.Decode(&req); err != nil {
			openAIError(w, http.StatusBadRequest, "invalid_request_error", "the request, as OpenAI's JSON: "+err.Error())
			return
		}
		if m, _ := req["model"].(string); m == "" {
			if upstream == "/embeddings" {
				req["model"] = "embedding"
			} else {
				req["model"] = "default"
			}
		}
		req["user"] = who // the spend is the caller's, whoever the request says
		model, _ = req["model"].(string)
		stream, _ = req["stream"].(bool)
		body, _ = json.Marshal(req)
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, strings.TrimRight(conn["llmUrl"], "/")+upstream, bytes.NewReader(body))
	if err != nil {
		openAIError(w, http.StatusBadGateway, "gateway_error", err.Error())
		return
	}
	out.Header.Set("Authorization", "Bearer "+conn["llmKey"])
	if body != nil {
		out.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(out)
	if err != nil {
		openAIError(w, http.StatusBadGateway, "gateway_error", "the gateway: "+err.Error())
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "x-litellm-response-cost"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if stream {
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				break
			}
		}
	} else {
		io.Copy(w, io.LimitReader(resp.Body, 64<<20))
	}
	if r.Method == http.MethodPost && resp.StatusCode == http.StatusOK {
		cost, _ := strconv.ParseFloat(resp.Header.Get("x-litellm-response-cost"), 64)
		detail := fmt.Sprintf("%s, through its OpenAI route: %.4f spent", model, cost)
		if stream {
			detail = model + ", through its OpenAI route, streamed"
		}
		s.record(r, who, "llm.asked", "project:"+v.Name, detail)
	}
}

// openAIError answers as OpenAI's API does, so its clients read the reason.
func openAIError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": kind, "code": status}})
}
