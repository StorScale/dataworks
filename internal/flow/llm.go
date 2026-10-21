package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The llm step (R11.8): a column asked of a model through the platform's
// gateway (use case 12.1), on the project's key and budget, under the
// guardrail: classify each row's value into labels, extract something from
// it, or summarize it. The answers are a new column. The step isn't SQL:
// the platform asks the model (platformd, in batches) and writes the
// answers as a table in the project's namespace, which the step's dbt
// model reads as a source; the DAG asks before it builds.

// The modes. nearest isn't a question: each value is embedded and searched
// in a knowledge bank of the project's (12.6), for the closest passage.
var LLMModes = []string{"classify", "extract", "summarize", "nearest"}

const (
	llmBatch    = 20   // rows a question carries
	llmMaxRows  = 1000 // rows a run reads (the platform's SQL answers that many at most)
	llmMaxValue = 2000 // characters of a value sent
)

// compileLLM checks an llm step and types its result: the input's columns
// and the answer's. Its SQL reads the table the platform writes.
func compileLLM(s *Step, in []Column, env Env, r refs) ([]Column, string, error) {
	if !slices.Contains(LLMModes, s.Mode) {
		return nil, "", fmt.Errorf("an llm step's mode is classify, extract, summarize or nearest, not %q", s.Mode)
	}
	if _, err := need(in, s.Column, "llm"); err != nil {
		return nil, "", err
	}
	if !NameOK(s.Name) {
		return nil, "", fmt.Errorf("the answer's column is lowercase letters, digits and underscores: %q isn't", s.Name)
	}
	if _, dup := find(in, s.Name); dup {
		return nil, "", fmt.Errorf("the input already has a column %s", s.Name)
	}
	switch s.Mode {
	case "classify":
		if len(s.Values) < 2 {
			return nil, "", fmt.Errorf("classify needs at least two labels")
		}
		for _, v := range s.Values {
			if strings.TrimSpace(v) == "" || strings.ContainsAny(v, "\"\n") {
				return nil, "", fmt.Errorf("a label is a word or a few: %q isn't", v)
			}
		}
	case "extract":
		if strings.TrimSpace(s.Prompt) == "" {
			return nil, "", fmt.Errorf("extract needs a prompt saying what to extract")
		}
	case "nearest":
		if !NameOK(s.Bank) {
			return nil, "", fmt.Errorf("nearest needs the knowledge bank to search (bank)")
		}
		if _, dup := find(in, s.Name+"_source"); dup {
			return nil, "", fmt.Errorf("the input already has a column %s_source", s.Name)
		}
	}
	if s.Limit < 0 || s.Limit > llmMaxRows {
		return nil, "", fmt.Errorf("an llm step reads at most %d rows a run (filter first)", llmMaxRows)
	}
	if s.Catalog == "" {
		s.Catalog = "iceberg"
	}
	if s.Namespace == "" {
		s.Namespace = env.Namespace
	}
	if s.Table == "" {
		s.Table = ModelName(r.flow, s)
	}
	out := append(slices.Clone(in), Column{Name: s.Name, Type: "varchar"})
	if s.Mode == "nearest" {
		out = append(out, Column{Name: s.Name + "_source", Type: "varchar"})
	}
	return out, "select * from " + r.table(s), nil
}

// An Asker asks the gateway one question and gives the answer's text and
// what it cost (LiteLLM says in a header; the stand-in too).
type Asker interface {
	Ask(ctx context.Context, prompt string) (string, float64, error)
}

// GatewayAsker asks the platform's gateway (LiteLLM or its stand-in) on a key.
type GatewayAsker struct {
	URL   string
	Key   string
	User  string // who the spend is recorded for
	Model string // "" is default
	HTTP  *http.Client
}

func (g *GatewayAsker) Ask(ctx context.Context, prompt string) (string, float64, error) {
	model := g.Model
	if model == "" {
		model = "default"
	}
	// No temperature: the model's default (some, claude-opus-5 among them, take no other).
	body, _ := json.Marshal(map[string]any{"model": model, "user": g.User,
		"messages": []map[string]string{{"role": "user", "content": prompt}}})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(g.URL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+g.Key)
	req.Header.Set("Content-Type", "application/json")
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("the gateway: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Error any `json:"error"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Error != nil {
			switch v := e.Error.(type) {
			case string:
				msg = v
			case map[string]any:
				if m, ok := v["message"].(string); ok {
					msg = m
				}
			}
		}
		return "", 0, fmt.Errorf("the gateway refused (HTTP %d): %s", resp.StatusCode, msg)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return "", 0, fmt.Errorf("the gateway's answer isn't a completion")
	}
	cost, _ := strconv.ParseFloat(resp.Header.Get("x-litellm-response-cost"), 64)
	return out.Choices[0].Message.Content, cost, nil
}

// An LLMResult is what asking did: the rows with the answer's column added,
// what it cost, and how many answers couldn't be read.
type LLMResult struct {
	Rows     [][]any
	Cost     float64
	Batches  int
	Unread   int // rows whose answer wasn't one of the labels, or wasn't given
	Warnings []string
}

// Prompt is the question for a batch of values: the mode's instruction, the
// step's prompt, the shape of the answer, and the values numbered.
func (s *Step) LLMPrompt(values []string) string {
	var b strings.Builder
	switch s.Mode {
	case "classify":
		fmt.Fprintf(&b, "Classify each item below as exactly one of: %s.\n", strings.Join(s.Values, ", "))
	case "extract":
		b.WriteString("For each item below, extract what is asked.\n")
	case "summarize":
		b.WriteString("Summarize each item below in one sentence.\n")
	}
	if p := strings.TrimSpace(s.Prompt); p != "" {
		b.WriteString(p + "\n")
	}
	fmt.Fprintf(&b, "Answer with a JSON array of %d strings, one per item, in order, and nothing else", len(values))
	if s.Mode == "classify" {
		b.WriteString(" (each string one of the labels)")
	} else if s.Mode == "extract" {
		b.WriteString(" (an empty string when there is nothing)")
	}
	b.WriteString(".\n")
	for i, v := range values {
		v = strings.Join(strings.Fields(v), " ")
		if len(v) > llmMaxValue {
			v = v[:llmMaxValue] + "…"
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, v)
	}
	return b.String()
}

// parseAnswers reads the model's JSON array of n strings; anything else is an error.
func parseAnswers(text string, n int) ([]string, error) {
	i, j := strings.Index(text, "["), strings.LastIndex(text, "]")
	if i < 0 || j <= i {
		return nil, fmt.Errorf("no JSON array in the answer")
	}
	var raw []any
	if err := json.Unmarshal([]byte(text[i:j+1]), &raw); err != nil {
		return nil, fmt.Errorf("the answer isn't a JSON array: %v", err)
	}
	if len(raw) != n {
		return nil, fmt.Errorf("the answer has %d items for %d values", len(raw), n)
	}
	out := make([]string, n)
	for k, v := range raw {
		switch x := v.(type) {
		case string:
			out[k] = x
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out, nil
}

// LLMApply asks the model about each row's value, in batches, and adds the
// answers as the last column. A batch the gateway refuses (the guardrail,
// the budget) fails the whole, saying why.
func LLMApply(ctx context.Context, s *Step, columns []string, rows [][]any, ask Asker) (*LLMResult, error) {
	col := slices.Index(columns, s.Column)
	if col < 0 {
		return nil, fmt.Errorf("no column %s in the rows", s.Column)
	}
	res := &LLMResult{Rows: make([][]any, 0, len(rows))}
	labels := map[string]string{}
	for _, v := range s.Values {
		labels[strings.ToLower(strings.TrimSpace(v))] = v
	}
	for start := 0; start < len(rows); start += llmBatch {
		end := min(start+llmBatch, len(rows))
		batch := rows[start:end]
		values := make([]string, len(batch))
		for i, row := range batch {
			if row[col] != nil {
				values[i] = fmt.Sprint(row[col])
			}
		}
		text, cost, err := ask.Ask(ctx, s.LLMPrompt(values))
		res.Cost += cost
		res.Batches++
		if err != nil {
			return res, fmt.Errorf("rows %d to %d: %w", start+1, end, err)
		}
		answers, err := parseAnswers(text, len(values))
		if err != nil {
			res.Unread += len(batch)
			res.Warnings = append(res.Warnings, fmt.Sprintf("rows %d to %d: %v; their answers are empty", start+1, end, err))
			answers = make([]string, len(values))
		}
		for i, row := range batch {
			var a any
			if ans := strings.TrimSpace(answers[i]); ans != "" {
				if s.Mode == "classify" {
					if l, ok := labels[strings.ToLower(ans)]; ok {
						a = l
					} else {
						res.Unread++
					}
				} else {
					a = ans
				}
			}
			res.Rows = append(res.Rows, append(slices.Clone(row), a))
		}
	}
	return res, nil
}

// Embed is the gateway's embeddings of the texts, in order, on the key (the
// model named "embedding" unless told another), and what they cost.
func (g *GatewayAsker) Embed(ctx context.Context, model string, texts []string) ([][]float64, float64, error) {
	if model == "" {
		model = "embedding"
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": texts, "user": g.User})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(g.URL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+g.Key)
	req.Header.Set("Content-Type", "application/json")
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("the gateway: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Error any `json:"error"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil {
			if m, ok := e.Error.(map[string]any); ok {
				if t, ok := m["message"].(string); ok {
					msg = t
				}
			}
		}
		return nil, 0, fmt.Errorf("the gateway refused (HTTP %d): %s", resp.StatusCode, msg)
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Data) != len(texts) {
		return nil, 0, fmt.Errorf("the gateway's answer isn't %d embeddings", len(texts))
	}
	vecs := make([][]float64, len(texts))
	for _, d := range out.Data {
		if d.Index >= 0 && d.Index < len(vecs) {
			vecs[d.Index] = d.Embedding
		}
	}
	cost, _ := strconv.ParseFloat(resp.Header.Get("x-litellm-response-cost"), 64)
	return vecs, cost, nil
}

// Chunks are a text in pieces of about size characters, cut at a sentence
// or a space where one is near, so each is embedded whole.
func Chunks(text string, size int) []string {
	text = strings.Join(strings.Fields(text), " ")
	var out []string
	for len(text) > size {
		cut := strings.LastIndexAny(text[:size], ".!?")
		if cut < size/2 {
			cut = strings.LastIndex(text[:size], " ")
		}
		if cut < size/2 {
			cut = size - 1
		}
		out = append(out, strings.TrimSpace(text[:cut+1]))
		text = strings.TrimSpace(text[cut+1:])
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

// Chat is one round of a conversation with tools (an agent's, 12.7): the
// messages so far and the tools the model may call, in the gateway's
// (OpenAI's) shape; the model's message back (its content, or the tools it
// calls) and what it cost.
func (g *GatewayAsker) Chat(ctx context.Context, messages []map[string]any, tools []map[string]any) (map[string]any, float64, error) {
	model := g.Model
	if model == "" {
		model = "default"
	}
	in := map[string]any{"model": model, "user": g.User, "messages": messages}
	if len(tools) > 0 {
		in["tools"] = tools
	}
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(g.URL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+g.Key)
	req.Header.Set("Content-Type", "application/json")
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("the gateway: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		msg := strings.TrimSpace(string(data))
		var e struct {
			Error any `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil {
			if m, ok := e.Error.(map[string]any); ok {
				if t, ok := m["message"].(string); ok {
					msg = t
				}
			} else if t, ok := e.Error.(string); ok {
				msg = t
			}
		}
		return nil, 0, fmt.Errorf("the gateway refused (HTTP %d): %s", resp.StatusCode, msg)
	}
	var out struct {
		Choices []struct {
			Message map[string]any `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 || out.Choices[0].Message == nil {
		return nil, 0, fmt.Errorf("the gateway's answer isn't a completion")
	}
	cost, _ := strconv.ParseFloat(resp.Header.Get("x-litellm-response-cost"), 64)
	return out.Choices[0].Message, cost, nil
}
