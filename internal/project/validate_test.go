package project

import (
	"strings"
	"testing"
)

// Each area's check, on a project that's fine but for one thing, says what it is.
func TestValidators(t *testing.T) {
	base := `{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"}],"tables":{}%s}}`
	for _, tc := range []struct{ area, extra, want string }{
		{"members", `,"members":[]`, "at least one member"},
		{"members", `,"members":[{"user":"bob","group":"x","role":"editor"}]`, "a group or a user, not both"},
		{"members", `,"members":[{"user":"bob","role":"owner"}]`, `role "owner" must be reader or editor`},
		{"tables", `,"tables":{"catalog":"Bad Name"}`, "must be lowercase identifiers"},
		{"tables", `,"tables":{"readers":{"masks":[{"table":"t","column":"c","type":"blur"}]}}`, "mask type"},
		{"tables", `,"tables":{"checks":[{"table":"t","check":"rowCount"}]}`, "rowCount needs a min, a max, or both"},
		{"files", `,"files":{"bucket":"No"}`, "files: bucket"},
		{"pipelines", `,"pipelines":{"serviceAccount":"not a client"}`, "isn't a Keycloak client ID"},
		{"products", `,"products":[{"name":"p","table":"t","owner":"","version":"1"}]`, "products: p"},
		{"semantic", `,"semantic":{"uses":["sales"]}`, "doesn't build on itself"},
		{"history", `,"tables":{"freshness":[{"table":"t","within":"soon"}]}`, "as 30m, 24h or 7d"},
		{"history", `,"tables":{"people":[{"table":"t","column":"c"},{"table":"t","column":"d"}]}`, "people: t twice"},
		{"history", `,"tables":{"tags":[{"name":"v1","commit":"zz","keepUntil":"2033-10-03"}]}`, "isn't a Nessie commit"},
		{"notify", `,"notify":{"email":["nobody"]}`, "isn't an email address"},
		{"retention", `,"tables":{"retention":[{"table":"t","column":"c","keep":"forever"}]}`, "as 730d or 2y"},
		{"sources", `,"sources":[{"name":"crm","kind":"access","connection":"c"}]`, `kind "access"`},
		{"sources", `,"sources":[{"name":"crm","kind":"postgresql","connection":"c","masks":[{"table":"s.t","column":"c","type":"hash"}]}]`, "readers: true"},
		{"imports", `,"imports":[{"name":"crm","source":"ftp"}]`, `source "ftp"`},
		{"imports", `,"imports":[{"name":"a","source":"files","files":{"tables":[{"name":"x","file":"x.csv"}]}},{"name":"a","source":"files","files":{"tables":[{"name":"x","file":"x.csv"}]}}]`, `"a" twice`},
		{"deliveries", `,"deliveries":[{"name":"d","table":"t","format":"csv","files":"d-{date}.csv","schedule":"daily","target":"sftp"}]`, "five cron fields"},
		{"deliveries", `,"deliveries":[{"name":"d","table":"t","query":"select 1","format":"csv","files":"d-{date}.csv","schedule":"0 6 * * 1","target":"sftp"}]`, "a table or a query, one of them"},
		{"limits", `,"limits":{"queries":500}`, "from 1 to 100 at once"},
		{"limits", `,"limits":{"storage":"500Gi"}`, "storage needs files"},
	} {
		_, err := Parse([]byte(strings.Replace(base, "%s", tc.extra, 1)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %s\n got %v\nwant %q", tc.area, tc.extra, err, tc.want)
		}
	}
	// Problems are gathered, not stopped at the first.
	_, err := Parse([]byte(strings.Replace(base, "%s", `,"members":[],"notify":{"email":["nobody"]},"limits":{"queries":500}`, 1)))
	if err == nil || strings.Count(err.Error(), "; ") != 2 {
		t.Errorf("all at once: %v", err)
	}
}
