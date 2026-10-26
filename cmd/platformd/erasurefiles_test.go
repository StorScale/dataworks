package main

import (
	"strings"
	"testing"

	"github.com/StorScale/dataworks/internal/project"
)

func TestAnErasureReachesTheLandingFiles(t *testing.T) {
	p, err := project.Parse([]byte(`{"metadata":{"name":"retail"},"spec":{"members":[{"user":"bob","role":"editor"}],"files":{},
		"pipelines":{"serviceAccount":"airflow-pipelines"},
		"tables":{"people":[{"table":"uploads_customers","column":"customer_id"},{"table":"uploads_orders","column":"customer_id"}]},
		"imports":[{"name":"uploads","source":"files","files":{"tables":[{"name":"customers","file":"customers.csv"},
			{"name":"orders","file":"orders-*.jsonl"},{"name":"campaigns","file":"campaigns.csv"}]}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := landingFiles(p); len(got) != 2 || got["customers.csv"] != "customer_id" || got["orders-*.jsonl"] != "customer_id" {
		t.Errorf("the files feeding tables of people: %v", got)
	}

	csvIn := "customer_id,first_name,email\n34,Ana,ana@example.com\n35,Ben,\"ben, jr@example.com\"\n 35 ,Ben again,b@example.com\n36,Cy,cy@example.com\n"
	out, n, err := withoutPersonCSV([]byte(csvIn), "customer_id", "35")
	if err != nil || n != 2 || strings.Contains(string(out), "Ben") || !strings.Contains(string(out), "34,Ana") || !strings.HasPrefix(string(out), "customer_id,first_name,email\n") {
		t.Errorf("CSV: %d %v\n%s", n, err, out)
	}
	if _, _, err := withoutPersonCSV([]byte("id,name\n35,Ben\n"), "customer_id", "35"); err == nil {
		t.Error("a file without the column wasn't said so")
	}
	jsonl := `{"order_id":1,"customer_id":35,"amount":9.5}` + "\n" + `{"order_id":2,"customer_id":36}` + "\n" + `{"order_id":3,"customer_id":"35"}` + "\n"
	out, n, err = withoutPersonJSONL([]byte(jsonl), "customer_id", "35")
	if err != nil || n != 2 || strings.Count(string(out), "\n") != 1 || !strings.Contains(string(out), `"customer_id":36`) {
		t.Errorf("JSON lines: %d %v\n%s", n, err, out)
	}
}
