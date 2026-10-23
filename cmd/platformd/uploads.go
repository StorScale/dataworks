package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/StorScale/storscale-platform/internal/project"
)

// Uploads are files put in a project's landing area (use case 11.1, R11.3):
// landing/uploads/ in the project's bucket, where its pipelines read. The
// platform writes them as the person (their Keycloak token exchanged with
// Buckets' STS, so the bucket's own policy decides: editors write, readers
// read), previews a CSV's first rows with the types it reads, and a files
// import (8.2) makes a file a table the canvas takes as an input.

const (
	uploadsPrefix = "landing/uploads/"
	uploadMax     = 256 << 20 // bytes
	previewBytes  = 64 << 10
	previewRowsN  = 20
)

var uploadNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,120}$`)

// bucketAs is the project's bucket as the person: a client on Buckets with
// credentials from their token.
func (s *server) bucketAs(ctx context.Context, token string) (*minio.Client, error) {
	if s.cfg.StoreURL == "" {
		return nil, errors.New("the platform keeps no files here (no STORE_URL)")
	}
	if token == "" {
		return nil, errors.New("no token to do it as you: sign in again")
	}
	u, err := url.Parse(s.cfg.StoreURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("the store's address isn't a URL")
	}
	creds, err := credentials.NewSTSWebIdentity(s.cfg.StoreURL, func() (*credentials.WebIdentityToken, error) {
		return &credentials.WebIdentityToken{Token: token, Expiry: 3600}, nil
	})
	if err != nil {
		return nil, err
	}
	return minio.New(u.Host, &minio.Options{Creds: creds, Secure: u.Scheme == "https", Region: "us-east-1", BucketLookup: minio.BucketLookupPath})
}

// bucketOf is the project's bucket: its name.
func bucketOf(p *project.Project) string { return p.Metadata.Name }

type upload struct {
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	At     time.Time `json:"at"`
	Format string    `json:"format,omitempty"` // csv, xlsx, jsonl, parquet, or "" (not one an import reads)
}

// handleUploads lists a project's uploads, as the person.
func (s *server) handleUploads(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.freshPerson(w, r)
	if !ok {
		return
	}
	p, _, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	c, err := s.bucketAs(r.Context(), token)
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, err.Error())
		return
	}
	out := []upload{}
	for o := range c.ListObjects(r.Context(), bucketOf(p), minio.ListObjectsOptions{Prefix: uploadsPrefix}) {
		if o.Err != nil {
			s.fail(w, r, http.StatusBadGateway, "Buckets: "+o.Err.Error())
			return
		}
		name := strings.TrimPrefix(o.Key, uploadsPrefix)
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		out = append(out, upload{Name: name, Size: o.Size, At: o.LastModified, Format: project.FileFormat(name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	writeJSON(w, http.StatusOK, map[string]any{"uploads": out, "prefix": uploadsPrefix, "bucket": bucketOf(p)})
}

// handleUpload puts a file in the project's landing area, as the person
// (an editor: the bucket refuses a reader), and answers with its preview.
func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.freshPerson(w, r)
	if !ok {
		return
	}
	p, _, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	if !s.memberCan(r, p.Metadata.Name, who, project.PermWriteFiles) {
		s.recordRefusal(r, who, "upload", "project:"+p.Metadata.Name, "not permitted (write-files)")
		s.fail(w, r, http.StatusForbidden, "the project's editors upload files to it")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, uploadMax+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		s.fail(w, r, http.StatusBadRequest, "a file, as multipart form data (file), up to 256 MB: "+err.Error())
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "a file, as multipart form data (file)")
		return
	}
	defer f.Close()
	name := path.Base(hdr.Filename)
	if given := r.FormValue("name"); given != "" {
		name = given
	}
	if !uploadNameRE.MatchString(name) || name == "." || name == ".." {
		s.fail(w, r, http.StatusUnprocessableEntity, "a file's name is letters, digits, dots, dashes and underscores: "+name)
		return
	}
	c, err := s.bucketAs(r.Context(), token)
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, err.Error())
		return
	}
	head := make([]byte, previewBytes)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	body := io.MultiReader(bytes.NewReader(head), f)
	size := hdr.Size
	if size <= 0 {
		size = -1
	}
	info, err := c.PutObject(r.Context(), bucketOf(p), uploadsPrefix+name, body, size, minio.PutObjectOptions{ContentType: hdr.Header.Get("Content-Type")})
	if err != nil {
		code := http.StatusBadGateway
		if resp := minio.ToErrorResponse(err); resp.Code == "AccessDenied" {
			code = http.StatusForbidden
			err = errors.New("Buckets refused it: the bucket's policy doesn't let you write it")
		}
		s.fail(w, r, code, err.Error())
		return
	}
	s.record(r, who, "upload", "project:"+p.Metadata.Name, fmt.Sprintf("%s (%d bytes)", name, info.Size))
	up := upload{Name: name, Size: info.Size, At: time.Now().UTC(), Format: project.FileFormat(name)}
	writeJSON(w, http.StatusOK, map[string]any{"upload": up, "preview": previewOf(name, head, n == previewBytes), "table": tableNameOf(name)})
}

// handleUploadPreview is an upload's first rows, read again as the person.
func (s *server) handleUploadPreview(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.freshPerson(w, r)
	if !ok {
		return
	}
	p, _, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	name := r.PathValue("file")
	if !uploadNameRE.MatchString(name) {
		s.fail(w, r, http.StatusNotFound, "no such upload")
		return
	}
	c, err := s.bucketAs(r.Context(), token)
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, err.Error())
		return
	}
	obj, err := c.GetObject(r.Context(), bucketOf(p), uploadsPrefix+name, minio.GetObjectOptions{})
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	defer obj.Close()
	head := make([]byte, previewBytes)
	n, err := io.ReadFull(obj, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		code := http.StatusBadGateway
		if resp := minio.ToErrorResponse(err); resp.Code == "NoSuchKey" {
			code = http.StatusNotFound
		} else if resp.Code == "AccessDenied" {
			code = http.StatusForbidden
		}
		s.fail(w, r, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "preview": previewOf(name, head[:n], n == previewBytes), "table": tableNameOf(name)})
}

// handleDeleteUpload removes an upload, as the person.
func (s *server) handleDeleteUpload(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.freshPerson(w, r)
	if !ok {
		return
	}
	p, _, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	name := r.PathValue("file")
	if !s.memberCan(r, p.Metadata.Name, who, project.PermWriteFiles) || !uploadNameRE.MatchString(name) {
		s.fail(w, r, http.StatusForbidden, "the project's editors remove its uploads")
		return
	}
	c, err := s.bucketAs(r.Context(), token)
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err := c.RemoveObject(r.Context(), bucketOf(p), uploadsPrefix+name, minio.RemoveObjectOptions{}); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, who, "upload.removed", "project:"+p.Metadata.Name, name)
	w.WriteHeader(http.StatusNoContent)
}

// A preview is a file's first rows as the platform reads them: its columns
// (named by the header), the type each looks like, and the rows.
type preview struct {
	Columns []previewColumn `json:"columns"`
	Rows    [][]string      `json:"rows"`
	Partial bool            `json:"partial,omitempty"` // only the first part of the file was read
	Note    string          `json:"note,omitempty"`    // why there's no preview
}

type previewColumn struct {
	Name string `json:"name"`
	Type string `json:"type"` // bigint, double, boolean, date, timestamp, varchar
}

var tableNotRE = regexp.MustCompile(`[^a-z0-9_]+`)

// tableNameOf is the table a file would become: suppliers.csv -> suppliers.
func tableNameOf(file string) string {
	n := strings.TrimSuffix(file, path.Ext(file))
	n = strings.Trim(tableNotRE.ReplaceAllString(strings.ToLower(n), "_"), "_")
	if n == "" || n[0] >= '0' && n[0] <= '9' {
		n = "file_" + n
	}
	if len(n) > 60 {
		n = n[:60]
	}
	return n
}

// previewOf reads a file's head: a CSV's first rows (the header named, the
// types guessed from the values); other formats say they're read once imported.
func previewOf(name string, head []byte, partial bool) preview {
	switch project.FileFormat(name) {
	case "csv":
		return previewCSV(head, partial)
	case "":
		return preview{Note: "not a file an import reads (csv, xlsx, jsonl, parquet)"}
	}
	return preview{Note: "previewed once imported (the platform reads " + project.FileFormat(name) + " in the import)"}
}

func previewCSV(head []byte, partial bool) preview {
	text := string(head)
	if partial { // the last line may be cut
		if i := strings.LastIndexByte(text, '\n'); i > 0 {
			text = text[:i]
		}
	}
	rd := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\ufeff")))
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true
	if strings.Count(firstLine(text), ";") > strings.Count(firstLine(text), ",") {
		rd.Comma = ';'
	}
	records, err := rd.ReadAll()
	if err != nil && len(records) == 0 {
		return preview{Note: "can't read it as CSV: " + err.Error()}
	}
	if len(records) == 0 {
		return preview{Note: "empty"}
	}
	pv := preview{Partial: partial}
	for i, h := range records[0] {
		n := tableNameOf(strings.TrimSpace(h))
		if strings.TrimSpace(h) == "" {
			n = "column_" + strconv.Itoa(i+1)
		}
		pv.Columns = append(pv.Columns, previewColumn{Name: n, Type: "varchar"})
	}
	rows := records[1:]
	for i := range pv.Columns {
		pv.Columns[i].Type = guessType(rows, i)
	}
	if len(rows) > previewRowsN {
		rows = rows[:previewRowsN]
	}
	pv.Rows = rows
	if pv.Rows == nil {
		pv.Rows = [][]string{}
	}
	return pv
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}

// guessType is the Trino type a column's values all fit, the narrowest
// first: bigint, double, boolean, date, timestamp; else varchar.
func guessType(rows [][]string, col int) string {
	kinds := []string{"bigint", "double", "boolean", "date", "timestamp"}
	fits := map[string]bool{}
	for _, k := range kinds {
		fits[k] = true
	}
	seen := false
	for _, r := range rows {
		if col >= len(r) || strings.TrimSpace(r[col]) == "" {
			continue
		}
		v := strings.TrimSpace(r[col])
		seen = true
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			fits["bigint"] = false
		}
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			fits["double"] = false
		}
		if l := strings.ToLower(v); l != "true" && l != "false" {
			fits["boolean"] = false
		}
		if _, err := time.Parse("2006-01-02", v); err != nil {
			fits["date"] = false
		}
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			if _, err := time.Parse("2006-01-02 15:04:05", v); err != nil {
				fits["timestamp"] = false
			}
		}
	}
	if !seen {
		return "varchar"
	}
	for _, k := range kinds {
		if fits[k] {
			return k
		}
	}
	return "varchar"
}
