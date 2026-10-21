package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/flow"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

// Knowledge banks (use case 12.6): a project's text, searched by meaning.
// Building one reads each source's rows as the caller (an editor, or the
// project's pipelines), cuts each text into chunks, embeds them through the
// gateway on the project's key, and writes them, with their vectors, as the
// table knowledge_<bank> in the project's namespace, as the caller: an
// ordinary table of the project's, under its rules. Searching embeds the
// question the same way and ranks the chunks in Trino by cosine similarity,
// as the person asking (or the agent, by its own token).

const (
	knowledgeChunk    = 1200 // characters a chunk
	knowledgeBatch    = 64   // texts an embedding request
	knowledgeMaxRows  = 20000
	knowledgeInsertBy = 20 // rows an INSERT (each carries its vector)
)

type knowledgeBank struct {
	p     *project.Project
	bank  project.KnowledgeBank
	cat   string
	ns    string
	table string // catalog.namespace.knowledge_<bank>, quoted
	key   map[string]string
}

// bankOf is the request's project and bank, with the project's gateway key.
func (s *server) bankOf(w http.ResponseWriter, r *http.Request) (*knowledgeBank, bool) {
	p, err := s.store.Project(r.Context(), r.PathValue("name"))
	if err != nil || p == nil {
		s.fail(w, r, http.StatusNotFound, "no such project")
		return nil, false
	}
	name := r.PathValue("bank")
	var bank *project.KnowledgeBank
	for i := range p.Spec.Knowledge {
		if p.Spec.Knowledge[i].Name == name {
			bank = &p.Spec.Knowledge[i]
		}
	}
	if bank == nil {
		s.fail(w, r, http.StatusNotFound, "the project has no knowledge bank "+name)
		return nil, false
	}
	conn, err := s.store.Connection(r.Context(), p.Metadata.Name, p.Metadata.Name+"-llm")
	if err != nil || conn["llmKey"] == "" || conn["llmUrl"] == "" {
		s.fail(w, r, http.StatusUnprocessableEntity, "the project has no gateway key yet: give it an LLM budget (llm.budget) and the operator makes one")
		return nil, false
	}
	cat, ns := "iceberg", strings.ReplaceAll(p.Metadata.Name, "-", "_")
	if t := p.Spec.Tables; t != nil {
		if t.Catalog != "" {
			cat = t.Catalog
		}
		if t.Namespace != "" {
			ns = t.Namespace
		}
	}
	return &knowledgeBank{p: p, bank: *bank, cat: cat, ns: ns, key: conn,
		table: trino.Quote(cat) + "." + trino.Quote(ns) + "." + trino.Quote(bank.KnowledgeTable())}, true
}

func (s *server) trinoAs(token string) (*trino.Client, error) {
	tc, err := trino.New(envOr("TRINO_URL", "https://trino:8443"), "", "", os.Getenv("TRINO_CA"))
	if err != nil {
		return nil, err
	}
	return tc.As(token), nil
}

// handleKnowledgeBuild builds a bank afresh: its editors, administrators, and the project's pipelines.
func (s *server) handleKnowledgeBuild(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.person(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	st, err := s.store.Status(ctx, r.PathValue("name"))
	if err != nil || st == nil {
		s.fail(w, r, http.StatusNotFound, "no such project")
		return
	}
	allowed := false
	for _, m := range st.Members {
		if m.Username == who && (m.Role == "editor" || m.Role == "pipelines") {
			allowed = true
		}
	}
	if sess := s.session(r); sess != nil && s.isAdmin(sess.User) {
		allowed = true
	}
	if !allowed {
		s.recordRefusal(r, who, "knowledge.build", "project:"+r.PathValue("name"), "not an editor or the project's pipelines")
		s.fail(w, r, http.StatusForbidden, "the project's editors and its pipelines build its knowledge banks")
		return
	}
	kb, ok := s.bankOf(w, r)
	if !ok {
		return
	}
	tc, err := s.trinoAs(token)
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "Trino: "+err.Error())
		return
	}
	type chunk struct {
		source, ref, text string
		n                 int
	}
	var chunks []chunk
	for _, src := range kb.bank.Tables {
		from := trino.Quote(kb.cat) + "." + trino.Quote(kb.ns) + "." + trino.Quote(src.Table)
		rows, err := tc.Exec(ctx, fmt.Sprintf("SELECT CAST(%s AS varchar), CAST(%s AS varchar) FROM %s WHERE %s IS NOT NULL LIMIT %d",
			trino.Quote(src.ID), trino.Quote(src.Column), from, trino.Quote(src.Column), knowledgeMaxRows))
		if err != nil {
			s.fail(w, r, http.StatusUnprocessableEntity, "reading "+src.Table+"."+src.Column+": "+err.Error())
			return
		}
		for _, row := range rows {
			ref, _ := row[0].(string)
			text, _ := row[1].(string)
			for i, c := range flow.Chunks(text, knowledgeChunk) {
				chunks = append(chunks, chunk{source: src.Table + "." + src.Column, ref: ref, text: c, n: i})
			}
		}
	}
	asker := &flow.GatewayAsker{URL: kb.key["llmUrl"], Key: kb.key["llmKey"], User: who}
	vecs := make([][]float64, 0, len(chunks))
	cost := 0.0
	for start := 0; start < len(chunks); start += knowledgeBatch {
		end := min(start+knowledgeBatch, len(chunks))
		texts := make([]string, 0, end-start)
		for _, c := range chunks[start:end] {
			texts = append(texts, c.text)
		}
		v, c, err := asker.Embed(ctx, "", texts)
		cost += c
		if err != nil {
			why := err.Error()
			if strings.Contains(why, "guardrail") {
				why += " (mask such values in the source first: a prepare step, or a view)"
			}
			s.fail(w, r, http.StatusUnprocessableEntity, fmt.Sprintf("embedding chunks %d to %d: %s", start+1, end, why))
			return
		}
		vecs = append(vecs, v...)
	}
	if _, err := tc.Exec(ctx, "CREATE OR REPLACE TABLE "+kb.table+" (source varchar, ref varchar, chunk integer, text varchar, embedding array(double))"); err != nil {
		s.fail(w, r, http.StatusBadGateway, "making "+kb.bank.KnowledgeTable()+": "+err.Error())
		return
	}
	for start := 0; start < len(chunks); start += knowledgeInsertBy {
		end := min(start+knowledgeInsertBy, len(chunks))
		var vals []string
		for i := start; i < end; i++ {
			c := chunks[i]
			vals = append(vals, fmt.Sprintf("(%s, %s, %d, %s, %s)", sqlString(c.source), sqlString(c.ref), c.n, sqlString(c.text), sqlVector(vecs[i])))
		}
		if _, err := tc.Exec(ctx, "INSERT INTO "+kb.table+" VALUES "+strings.Join(vals, ", ")); err != nil {
			s.fail(w, r, http.StatusBadGateway, "writing "+kb.bank.KnowledgeTable()+": "+err.Error())
			return
		}
	}
	s.record(r, who, "knowledge.built", "project:"+kb.p.Metadata.Name+"/knowledge:"+kb.bank.Name, fmt.Sprintf("%d chunks, %.6f spent", len(chunks), cost))
	s.log.Info("knowledge bank built", "project", kb.p.Metadata.Name, "bank", kb.bank.Name, "user", who, "chunks", len(chunks), "cost", cost)
	writeJSON(w, http.StatusOK, map[string]any{"table": kb.cat + "." + kb.ns + "." + kb.bank.KnowledgeTable(), "chunks": len(chunks), "cost": cost})
}

// knowledgeHit is one chunk a search found.
type knowledgeHit struct {
	Source string  `json:"source"` // table.column
	Ref    string  `json:"ref"`    // the row's id
	Text   string  `json:"text"`
	Score  float64 `json:"score"` // cosine similarity, 1 the closest
}

// handleKnowledgeSearch is a bank's chunks closest in meaning to a question,
// as the person (or agent) asking: a member, under the project's rules on the bank's table.
func (s *server) handleKnowledgeSearch(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.person(w, r)
	if !ok {
		return
	}
	if _, _, ok := s.projectRole(w, r, who); !ok {
		return
	}
	var in struct {
		Query string `json:"query"`
		K     int    `json:"k"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Query) == "" {
		s.fail(w, r, http.StatusBadRequest, "a question, as JSON: {\"query\": ..., \"k\": 5}")
		return
	}
	if in.K <= 0 || in.K > 50 {
		in.K = 5
	}
	kb, ok := s.bankOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	asker := &flow.GatewayAsker{URL: kb.key["llmUrl"], Key: kb.key["llmKey"], User: who}
	v, cost, err := asker.Embed(ctx, "", []string{in.Query})
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "embedding the question: "+err.Error())
		return
	}
	tc, err := s.trinoAs(token)
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "Trino: "+err.Error())
		return
	}
	rows, err := tc.Exec(ctx, fmt.Sprintf("SELECT source, ref, text, cosine_similarity(embedding, %s) AS score FROM %s ORDER BY score DESC LIMIT %d",
		sqlVector(v[0]), kb.table, in.K))
	if err != nil {
		why := err.Error()
		if strings.Contains(why, "does not exist") {
			why = "the bank isn't built yet: build it from the project's Knowledge card"
		}
		s.fail(w, r, http.StatusUnprocessableEntity, why)
		return
	}
	hits := []knowledgeHit{}
	for _, row := range rows {
		h := knowledgeHit{}
		h.Source, _ = row[0].(string)
		h.Ref, _ = row[1].(string)
		h.Text, _ = row[2].(string)
		switch x := row[3].(type) {
		case float64:
			h.Score = x
		case string:
			h.Score, _ = strconv.ParseFloat(x, 64)
		}
		hits = append(hits, h)
	}
	writeJSON(w, http.StatusOK, map[string]any{"bank": kb.bank.Name, "hits": hits, "cost": cost})
}

// handleKnowledgeList is the project's banks, for its members: what each holds, and whether it's built.
func (s *server) handleKnowledgeList(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.person(w, r)
	if !ok {
		return
	}
	p, role, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	cat, ns := "iceberg", strings.ReplaceAll(p.Metadata.Name, "-", "_")
	if t := p.Spec.Tables; t != nil {
		if t.Catalog != "" {
			cat = t.Catalog
		}
		if t.Namespace != "" {
			ns = t.Namespace
		}
	}
	type bank struct {
		project.KnowledgeBank
		Table  string `json:"table"`
		Chunks *int64 `json:"chunks,omitempty"` // nil: not built (or not readable by the person)
	}
	out := []bank{}
	tc, _ := s.trinoAs(token)
	for _, k := range p.Spec.Knowledge {
		b := bank{KnowledgeBank: k, Table: cat + "." + ns + "." + k.KnowledgeTable()}
		if tc != nil {
			if rows, err := tc.Exec(r.Context(), "SELECT count(*) FROM "+trino.Quote(cat)+"."+trino.Quote(ns)+"."+trino.Quote(k.KnowledgeTable())); err == nil && len(rows) == 1 {
				if n, err := strconv.ParseInt(fmt.Sprint(rows[0][0]), 10, 64); err == nil {
					b.Chunks = &n
				}
			}
		}
		out = append(out, b)
	}
	writeJSON(w, http.StatusOK, map[string]any{"banks": out, "canBuild": role == "editor"})
}

func sqlString(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

// sqlVector is a vector as Trino's array(double) literal.
func sqlVector(v []float64) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(x, 'g', 9, 64)
	}
	return "CAST(ARRAY[" + strings.Join(parts, ",") + "] AS array(double))"
}
