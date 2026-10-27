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

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/trino"
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
		runs:    map[string]map[string]tableRun{}, wake: make(chan struct{}, 1)}
	s := &server{log: slog.New(slog.NewTextHandler(io.Discard, nil)), maintainer: m}
	p := &project.Project{}
	p.Metadata.Name = "sales"
	p.Spec.Tables = &project.Tables{Catalog: "iceberg", Namespace: "sales"}
	m.keepProject(context.Background(), s, p)

	want := []string{
		`ALTER TABLE "iceberg"."sales"."orders" EXECUTE optimize`,
		`ALTER TABLE "iceberg"."sales"."payroll" EXECUTE optimize`, // refused: told, and the next table kept
	}
	if strings.Join(ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(ran, "\n"), strings.Join(want, "\n"))
	}
	orders := m.runs["sales"]["orders"]
	if orders.Error != "" || len(orders.Done) != 1 || orders.After.DataFiles != 12 {
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

	// An adopted table: left to the platform it came from, which writes it.
	ran = nil
	p.Spec.Tables.Adopt = []project.Adoption{{Table: "orders", From: "lakehouse.sales.orders"}}
	m.keepProject(context.Background(), s, p)
	if len(ran) != 1 || strings.Contains(ran[0], `"orders"`) {
		t.Errorf("with orders adopted: %v (want payroll's optimize alone)", ran)
	}
	p.Spec.Tables.Adopt = nil

	// Paused.
	ran = nil
	p.Spec.Tables.Maintenance = &project.Maintenance{}
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
