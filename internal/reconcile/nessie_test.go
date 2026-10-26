package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/StorScale/dataworks/internal/project"
)

func TestAProjectsTagsAreAsItsSpecSays(t *testing.T) {
	var did []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v2/trees":
			w.Write([]byte(`{"references": [
				{"type": "BRANCH", "name": "main", "hash": "ffff0000ffff0000"},
				{"type": "TAG", "name": "sales.close-2026-08", "hash": "aaaa0000aaaa0000"},
				{"type": "TAG", "name": "sales.close-2026-09", "hash": "bbbb0000bbbb0000"},
				{"type": "TAG", "name": "sales.old", "hash": "cccc0000cccc0000"},
				{"type": "TAG", "name": "marketing.q3", "hash": "dddd0000dddd0000"}]}`))
		case r.Method == "POST":
			var to struct{ Hash string }
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &to)
			did = append(did, "tag "+r.URL.Query().Get("name")+" "+to.Hash)
			w.Write([]byte(`{"reference": {}}`))
		case r.Method == "DELETE":
			did = append(did, "untag "+strings.TrimPrefix(r.URL.EscapedPath(), "/api/v2/trees/"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	p, err := project.Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
		"spec":{"members":[{"group":"engineers","role":"editor"}],"tables":{"tags":[
			{"name":"close-2026-08","commit":"aaaa0000aaaa0000","keepUntil":"2033-09-03"},
			{"name":"close-2026-09","commit":"eeee0000eeee0000","keepUntil":"2033-10-03"},
			{"name":"close-2026-10","commit":"ffff0000ffff0000","keepUntil":"2033-11-03"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Nessie{URL: ts.URL}).syncTags(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	sort.Strings(did)
	want := []string{
		"tag sales.close-2026-09 eeee0000eeee0000", // moved: made again at its commit
		"tag sales.close-2026-10 ffff0000ffff0000", // new
		"untag sales.close-2026-09@bbbb0000bbbb0000",
		"untag sales.old@cccc0000cccc0000", // no longer listed
	}
	if strings.Join(did, "\n") != strings.Join(want, "\n") {
		t.Errorf("did:\n%s\nwant:\n%s", strings.Join(did, "\n"), strings.Join(want, "\n"))
	}
}
