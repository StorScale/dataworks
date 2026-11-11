package main

import (
	"strings"
	"testing"

	"github.com/StorScale/dataworks/internal/project"
)

func TestBucketOf(t *testing.T) {
	p := &project.Project{}
	p.Metadata.Name = "crm"
	if b := bucketOf(p); b != "" {
		t.Errorf("a project without files: bucket %q, want none", b)
	}
	p.Spec.Files = &project.Files{}
	if b := bucketOf(p); b != "crm" {
		t.Errorf("files without a bucket named: %q, want the project's name", b)
	}
	p.Spec.Files.Bucket = "crm-landing"
	if b := bucketOf(p); b != "crm-landing" {
		t.Errorf("files with a bucket named: %q, want crm-landing", b)
	}
}

func TestPreviewCSV(t *testing.T) {
	head := "\ufeffSupplier ID,Name,Price,Since,Active,Note\n1,Acme,10.5,2026-01-02,true,\n2,Globex,7,2026-02-03,false,\"a, b\"\n3,Initech,x"
	pv := previewCSV([]byte(head), true)
	cols := []string{}
	for _, c := range pv.Columns {
		cols = append(cols, c.Name+" "+c.Type)
	}
	if got := strings.Join(cols, ", "); got != "supplier_id bigint, name varchar, price double, since date, active boolean, note varchar" {
		t.Errorf("columns: %s", got)
	}
	if len(pv.Rows) != 2 || pv.Rows[1][5] != "a, b" || !pv.Partial {
		t.Errorf("rows: %v, partial %v", pv.Rows, pv.Partial)
	}
	if pv := previewCSV([]byte("a;b\n1;2\n"), false); len(pv.Columns) != 2 || pv.Columns[1].Type != "bigint" {
		t.Errorf("semicolons: %+v", pv)
	}
	if pv := previewOf("suppliers.xlsx", nil, false); !strings.Contains(pv.Note, "once imported") {
		t.Errorf("xlsx: %+v", pv)
	}
	if pv := previewOf("notes.txt", nil, false); !strings.Contains(pv.Note, "not a file an import reads") {
		t.Errorf("txt: %+v", pv)
	}
	if tableNameOf("Supplier Prices (2026).CSV") != "supplier_prices_2026" || tableNameOf("2026.csv") != "file_2026" {
		t.Errorf("table names: %s %s", tableNameOf("Supplier Prices (2026).CSV"), tableNameOf("2026.csv"))
	}
}
