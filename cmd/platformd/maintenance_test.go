package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/StorScale/storscale-platform/internal/catalog"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

func TestMaintenanceKeepsEveryTableInTurn(t *testing.T) {
	nessie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/iceberg/v1/config":
			w.Write([]byte(`{"overrides": {"prefix": "main%7Cwarehouse"}}`))
		case "/iceberg/v1/main%7Cwarehouse/namespaces/sales/tables":
			w.Write([]byte(`{"identifiers": [{"name": "orders"}, {"name": "payroll"}]}`))
		default:
			w.Write([]byte(`{"metadata": {"current-snapshot-id": 1, "snapshots": [{"snapshot-id": 1, "timestamp-ms": 1760000000000, "summary": {"total-data-files": "12"}}]}}`))
		}
	}))
	defer nessie.Close()
	var mu sync.Mutex
	var ran []string
	tr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sql, _ := io.ReadAll(r.Body)
		mu.Lock()
		ran = append(ran, string(sql))
		mu.Unlock()
		if strings.Contains(string(sql), `"payroll" EXECUTE optimize`) {
			w.Write([]byte(`{"id":"q","error":{"errorName":"PERMISSION_DENIED","message":"Access Denied: Cannot execute table procedure OPTIMIZE"}}`))
			return
		}
		w.Write([]byte(`{"id":"q","stats":{"state":"FINISHED"}}`))
	}))
	defer tr.Close()

	m := &maintainer{trino: &trino.Client{URL: tr.URL, User: "maintenance", Password: "p", HTTP: tr.Client()},
		iceberg: &catalog.Iceberg{HTTP: nessie.Client(), Base: nessie.URL + "/iceberg", Warehouse: "warehouse"},
		keep:    "7d", min: "1h", runs: map[string]map[string]tableRun{}, wake: make(chan struct{}, 1)}
	s := &server{log: slog.New(slog.NewTextHandler(io.Discard, nil)), maintainer: m}
	p := &project.Project{}
	p.Metadata.Name = "sales"
	p.Spec.Tables = &project.Tables{Catalog: "iceberg", Namespace: "sales", Maintenance: &project.Maintenance{KeepSnapshots: "30m"}}
	m.keepProject(context.Background(), s, p)

	want := []string{
		`ALTER TABLE "iceberg"."sales"."orders" EXECUTE optimize`,
		`ALTER TABLE "iceberg"."sales"."orders" EXECUTE expire_snapshots(retention_threshold => '3600s')`, // 30m: below Trino's least, so 1h
		`ALTER TABLE "iceberg"."sales"."orders" EXECUTE remove_orphan_files(retention_threshold => '3600s')`,
		`ALTER TABLE "iceberg"."sales"."payroll" EXECUTE optimize`, // refused: the table's other steps wait for the next run
	}
	if strings.Join(ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(ran, "\n"), strings.Join(want, "\n"))
	}
	orders := m.runs["sales"]["orders"]
	if orders.Error != "" || len(orders.Done) != 3 || orders.Keep != "1h" || orders.After.DataFiles != 12 {
		t.Errorf("orders: %+v", orders)
	}
	f := m.failures("sales")
	if len(f) != 1 || f[0].Table != "payroll" || !strings.Contains(f[0].Error, "optimize: PERMISSION_DENIED") {
		t.Errorf("failures: %+v", f)
	}
	if got := m.metrics(); !strings.Contains(got, `platform_table_maintenance_failed{project="sales",table="payroll"} 1`) ||
		!strings.Contains(got, `platform_table_maintenance_failed{project="sales",table="orders"} 0`) {
		t.Errorf("metrics:\n%s", got)
	}

	// Without compaction, and paused.
	ran = nil
	off := false
	p.Spec.Tables.Maintenance = &project.Maintenance{Compact: &off, KeepSnapshots: "14d"}
	m.keepProject(context.Background(), s, p)
	if len(ran) != 4 || strings.Contains(strings.Join(ran, " "), "optimize") || !strings.Contains(ran[0], "'1209600s'") {
		t.Errorf("compact false: %v", ran)
	}
	ran = nil
	p.Spec.Tables.Maintenance.Paused = true
	m.keepProject(context.Background(), s, p)
	if len(ran) != 0 {
		t.Errorf("paused: %v", ran)
	}
}

func TestAProjectIsQueuedOnce(t *testing.T) {
	m := &maintainer{wake: make(chan struct{}, 1)}
	if !m.enqueue("sales") || m.enqueue("sales") || m.state("sales") != "queued" || m.state("marketing") != "idle" {
		t.Errorf("queue: %v", m.queued)
	}
	m.running = "marketing"
	if m.enqueue("marketing") || m.state("marketing") != "running" {
		t.Error("a project being kept is queued again")
	}
}
