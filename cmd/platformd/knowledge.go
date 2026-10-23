package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/StorScale/storscale-platform/internal/flow"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

// Knowledge banks (use case 12.6): a project's text, searched by meaning.
// Building one reads each source's rows, and the text files in its folders
// of the project's bucket, as the caller (an editor, or the project's
// pipelines, on the bank's schedule), cuts each text into chunks, embeds them through the
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
	knowledgeMaxFiles = 2000
	knowledgeFileMax  = 4 << 20 // bytes of a file read
)

var (
	htmlDropRE = regexp.MustCompile(`(?is)<(script|style|head)\b.*?</(script|style|head)>`)
	htmlTagRE  = regexp.MustCompile(`(?s)<[^>]*>`)
)

// htmlText is an HTML page's text: its scripts, styles and head dropped, its tags taken out, its entities read.
func htmlText(page string) string {
	return html.UnescapeString(htmlTagRE.ReplaceAllString(htmlDropRE.ReplaceAllString(page, " "), " "))
}

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
	cat, ns := tablesOf(p)
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
		if m.Username == who && (m.Role == "pipelines" || project.Permits(m.Role, m.Can, project.PermBuild)) {
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
	files, skipped := 0, 0
	if len(kb.bank.Files) > 0 {
		bc, err := s.bucketAs(ctx, token)
		if err != nil {
			s.fail(w, r, http.StatusServiceUnavailable, err.Error())
			return
		}
		for _, f := range kb.bank.Files {
			for o := range bc.ListObjects(ctx, bucketOf(kb.p), minio.ListObjectsOptions{Prefix: f.Prefix, Recursive: true}) {
				if o.Err != nil {
					s.fail(w, r, http.StatusBadGateway, "reading the folder "+f.Prefix+": "+o.Err.Error())
					return
				}
				ext := strings.ToLower(path.Ext(o.Key))
				if !slices.Contains(project.KnowledgeFileFormats, ext) || o.Size > knowledgeFileMax || files >= knowledgeMaxFiles {
					skipped++
					continue
				}
				obj, err := bc.GetObject(ctx, bucketOf(kb.p), o.Key, minio.GetObjectOptions{})
				if err != nil {
					s.fail(w, r, http.StatusBadGateway, "reading "+o.Key+": "+err.Error())
					return
				}
				data, err := io.ReadAll(io.LimitReader(obj, knowledgeFileMax))
				obj.Close()
				if err != nil {
					s.fail(w, r, http.StatusBadGateway, "reading "+o.Key+": "+err.Error())
					return
				}
				text := string(data)
				if ext == ".html" || ext == ".htm" {
					text = htmlText(text)
				}
				for i, c := range flow.Chunks(text, knowledgeChunk) {
					chunks = append(chunks, chunk{source: "files:" + f.Prefix, ref: o.Key, text: c, n: i})
				}
				files++
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
	s.record(r, who, "knowledge.built", "project:"+kb.p.Metadata.Name+"/knowledge:"+kb.bank.Name, fmt.Sprintf("%d chunks (%d files), %.6f spent", len(chunks), files, cost))
	s.log.Info("knowledge bank built", "project", kb.p.Metadata.Name, "bank", kb.bank.Name, "user", who, "chunks", len(chunks), "files", files, "cost", cost)
	writeJSON(w, http.StatusOK, map[string]any{"table": kb.cat + "." + kb.ns + "." + kb.bank.KnowledgeTable(), "chunks": len(chunks), "files": files,
		"skipped": skipped, "cost": cost})
}

// knowledgeHit is one chunk a search found.
type knowledgeHit struct {
	Source string  `json:"source"` // table.column, or files:<folder>
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
	p, _, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	cat, ns := tablesOf(p)
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
	writeJSON(w, http.StatusOK, map[string]any{"banks": out, "canBuild": s.memberCan(r, p.Metadata.Name, who, project.PermBuild),
		// what a new bank needs, so the card can say: a budget to embed on, a bucket for files, pipelines for a schedule
		"hasBudget": p.Spec.LLM != nil && p.Spec.LLM.Budget > 0, "hasFiles": p.Spec.Files != nil, "hasPipelines": p.Spec.Pipelines != nil})
}

// handlePutKnowledge declares a bank, or changes one: the project's editors,
// as they register its agents. Its table is made when it's first built.
func (s *server) handlePutKnowledge(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, project.PermBuild, "declare its knowledge banks")
	if sess == nil {
		return
	}
	var k project.KnowledgeBank
	dec := json.NewDecoder(io.LimitReader(r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&k); err != nil {
		s.fail(w, r, http.StatusBadRequest, "a knowledge bank, as JSON: "+err.Error())
		return
	}
	k.Name = r.PathValue("bank")
	p := *v.Spec
	p.Spec.Knowledge = slices.Clone(p.Spec.Knowledge)
	i := slices.IndexFunc(p.Spec.Knowledge, func(x project.KnowledgeBank) bool { return x.Name == k.Name })
	if i < 0 {
		p.Spec.Knowledge = append(p.Spec.Knowledge, k)
	} else {
		p.Spec.Knowledge[i] = k
	}
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	var sources []string
	for _, t := range k.Tables {
		sources = append(sources, t.Table+"."+t.Column)
	}
	for _, f := range k.Files {
		sources = append(sources, "files in "+f.Prefix)
	}
	s.record(r, sess.User.Username, "knowledge.declared", "project:"+v.Name+"/knowledge:"+k.Name,
		map[bool]string{true: "declared ", false: "changed "}[i < 0]+k.Name+" from "+strings.Join(sources, ", ")+map[bool]string{true: ", built at " + k.Schedule, false: ""}[k.Schedule != ""])
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, k)
}

// handleDeleteKnowledge takes a bank out of the project: its editors. Its
// table stays, a table of the project's like any other, until they drop it.
func (s *server) handleDeleteKnowledge(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, project.PermBuild, "remove its knowledge banks")
	if sess == nil {
		return
	}
	name := r.PathValue("bank")
	p := *v.Spec
	before := len(p.Spec.Knowledge)
	p.Spec.Knowledge = slices.DeleteFunc(slices.Clone(p.Spec.Knowledge), func(x project.KnowledgeBank) bool { return x.Name == name })
	if len(p.Spec.Knowledge) == before {
		s.fail(w, r, http.StatusNotFound, "no such knowledge bank")
		return
	}
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(r, sess.User.Username, "knowledge.removed", "project:"+v.Name+"/knowledge:"+name, "its table knowledge_"+name+" kept")
	s.kick(r.Context(), v.Name)
	w.WriteHeader(http.StatusNoContent)
}

// tablesOf is the project's catalog and namespace.
func tablesOf(p *project.Project) (cat, ns string) {
	cat, ns = "iceberg", strings.ReplaceAll(p.Metadata.Name, "-", "_")
	if t := p.Spec.Tables; t != nil {
		if t.Catalog != "" {
			cat = t.Catalog
		}
		if t.Namespace != "" {
			ns = t.Namespace
		}
	}
	return cat, ns
}

// nearestPassages is the canvas's nearest step (an llm step's mode): each
// row's value embedded, as the step's other modes ask, and the closest
// passage of the bank found in Trino, as the token's owner; the passage and
// its source (table.column:ref, or files:<folder>:<key>) added to the row.
func (s *server) nearestPassages(ctx context.Context, token string, p *project.Project, asker *flow.GatewayAsker, st *flow.Step, columns []string, rows [][]any) (*flow.LLMResult, error) {
	if !slices.ContainsFunc(p.Spec.Knowledge, func(k project.KnowledgeBank) bool { return k.Name == st.Bank }) {
		return nil, fmt.Errorf("the project has no knowledge bank %s", st.Bank)
	}
	col := slices.Index(columns, st.Column)
	if col < 0 {
		return nil, fmt.Errorf("no column %s in the rows", st.Column)
	}
	tc, err := s.trinoAs(token)
	if err != nil {
		return nil, err
	}
	cat, ns := tablesOf(p)
	table := trino.Quote(cat) + "." + trino.Quote(ns) + "." + trino.Quote(project.KnowledgeBank{Name: st.Bank}.KnowledgeTable())
	res := &flow.LLMResult{Rows: make([][]any, 0, len(rows))}
	const batch = 20
	for start := 0; start < len(rows); start += batch {
		end := min(start+batch, len(rows))
		var texts []string
		var at []int // the batch's rows with a value
		for i, row := range rows[start:end] {
			if v := strings.TrimSpace(fmt.Sprint(row[col])); row[col] != nil && v != "" {
				texts, at = append(texts, v), append(at, i)
			}
		}
		found := map[int][2]any{}
		if len(texts) > 0 {
			vecs, cost, err := asker.Embed(ctx, "", texts)
			res.Cost += cost
			res.Batches++
			if err != nil {
				return res, fmt.Errorf("rows %d to %d: %w", start+1, end, err)
			}
			arr := make([]string, len(vecs))
			for i, v := range vecs {
				arr[i] = sqlVector(v)
			}
			got, err := tc.Exec(ctx, "SELECT q.i, max_by(k.text, cosine_similarity(k.embedding, q.v)), max_by(k.source || ':' || k.ref, cosine_similarity(k.embedding, q.v)) "+
				"FROM "+table+" k CROSS JOIN UNNEST(ARRAY["+strings.Join(arr, ", ")+"]) WITH ORDINALITY AS q(v, i) GROUP BY q.i")
			if err != nil {
				why := err.Error()
				if strings.Contains(why, "does not exist") {
					why = "the bank " + st.Bank + " isn't built yet: build it from the project's Knowledge card"
				}
				return res, fmt.Errorf("searching %s: %s", st.Bank, why)
			}
			for _, g := range got {
				if i, err := strconv.Atoi(fmt.Sprint(g[0])); err == nil && i >= 1 && i <= len(at) {
					found[at[i-1]] = [2]any{g[1], g[2]}
				}
			}
		}
		for i, row := range rows[start:end] {
			f, ok := found[i]
			if !ok {
				res.Unread++
			}
			res.Rows = append(res.Rows, append(slices.Clone(row), f[0], f[1]))
		}
	}
	return res, nil
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
