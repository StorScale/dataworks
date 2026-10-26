package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/minio/minio-go/v7"

	"github.com/StorScale/dataworks/internal/project"
)

// An erasure reaches the project's landing area too (6.9): the files its
// imports read into the tables that hold people (tables.people) are written
// again without the person's rows, so the next import doesn't bring them
// back. As the person erasing (Buckets' policies decide: the project's
// editors may write its files); a file that can't be rewritten (Excel,
// Parquet) or written is named in the record, never passed over.

type erasedFile struct {
	File   string `json:"file"`   // its key in the project's bucket
	Column string `json:"column"` // the column that identifies a person
	Rows   int64  `json:"rows"`   // the person's rows taken out
}

// landingFiles are the import files that feed the project's tables of
// people: file glob (under landing/uploads/) -> the identifying column.
func landingFiles(p *project.Project) map[string]string {
	out := map[string]string{}
	if p.Spec.Tables == nil {
		return out
	}
	people := map[string]string{}
	for _, pk := range p.Spec.Tables.People {
		people[pk.Table] = pk.Column
	}
	for _, im := range p.Spec.Imports {
		if im.Files == nil {
			continue
		}
		for _, f := range im.Files.Tables {
			if col, ok := people[im.Name+"_"+f.Name]; ok {
				out[f.File] = col
			}
		}
	}
	return out
}

// eraseFromLanding takes the person's rows out of the project's landing files.
func eraseFromLanding(ctx context.Context, c *minio.Client, p *project.Project, person string, errs map[string]string) []erasedFile {
	out := []erasedFile{}
	globs := landingFiles(p)
	if len(globs) == 0 {
		return out
	}
	bucket := bucketOf(p)
	for obj := range c.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: uploadsPrefix}) {
		if obj.Err != nil {
			errs["landing files"] = obj.Err.Error()
			return out
		}
		name := strings.TrimPrefix(obj.Key, uploadsPrefix)
		for glob, col := range globs {
			if ok, _ := path.Match(glob, name); !ok {
				continue
			}
			n, err := eraseFromFile(ctx, c, bucket, obj.Key, col, person)
			if err != nil {
				errs[obj.Key] = err.Error()
				break
			}
			out = append(out, erasedFile{File: obj.Key, Column: col, Rows: n})
			break
		}
	}
	return out
}

// eraseFromFile writes one file again without the rows whose column is the person.
func eraseFromFile(ctx context.Context, c *minio.Client, bucket, key, column, person string) (int64, error) {
	format := project.FileFormat(key)
	if format != "csv" && format != "jsonl" {
		return 0, fmt.Errorf("a %s file isn't rewritten here: take the person's rows out of it by hand, or remove it", format)
	}
	o, err := c.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return 0, err
	}
	data, err := io.ReadAll(io.LimitReader(o, uploadMax+1))
	o.Close()
	if err != nil {
		return 0, err
	}
	var kept []byte
	var n int64
	if format == "csv" {
		kept, n, err = withoutPersonCSV(data, column, person)
	} else {
		kept, n, err = withoutPersonJSONL(data, column, person)
	}
	if err != nil || n == 0 {
		return 0, err
	}
	ctype := map[string]string{"csv": "text/csv", "jsonl": "application/x-ndjson"}[format]
	if _, err := c.PutObject(ctx, bucket, key, bytes.NewReader(kept), int64(len(kept)), minio.PutObjectOptions{ContentType: ctype}); err != nil {
		return 0, fmt.Errorf("writing it again: %w", err)
	}
	return n, nil
}

func withoutPersonCSV(data []byte, column, person string) ([]byte, int64, error) {
	rd := csv.NewReader(bytes.NewReader(data))
	rd.FieldsPerRecord = -1
	rows, err := rd.ReadAll()
	if err != nil {
		return nil, 0, fmt.Errorf("not a CSV file: %w", err)
	}
	if len(rows) == 0 {
		return data, 0, nil
	}
	at := -1
	for i, h := range rows[0] {
		if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")), column) {
			at = i
		}
	}
	if at < 0 {
		return nil, 0, fmt.Errorf("no column %s in it", column)
	}
	out := rows[:1]
	var n int64
	for _, row := range rows[1:] {
		if at < len(row) && strings.TrimSpace(row[at]) == person {
			n++
			continue
		}
		out = append(out, row)
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.WriteAll(out); err != nil {
		return nil, 0, err
	}
	return b.Bytes(), n, nil
}

func withoutPersonJSONL(data []byte, column, person string) ([]byte, int64, error) {
	var b bytes.Buffer
	var n int64
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var row map[string]any
		if json.Unmarshal(line, &row) == nil && textOf(row[column]) == person {
			n++
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.Bytes(), n, sc.Err()
}

// textOf is a JSON value as the person's ID is written: 35, not 35.0.
func textOf(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}
