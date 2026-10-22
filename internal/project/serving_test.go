package project

import (
	"strings"
	"testing"
)

func TestServing(t *testing.T) {
	spec := `{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"}],"serving":[%s]}}`
	for _, tc := range []struct{ serving, want string }{
		{`{"name":"churn","model":"churn"}`, ""},
		{`{"name":"churn","model":"churn","memory":"2Gi","maxRows":500}`, ""},
		{`{"name":"Churn","model":"churn"}`, "up to 16 lowercase letters"},
		{`{"name":"a-name-far-too-long","model":"churn"}`, "up to 16 lowercase letters"},
		{`{"name":"churn","model":"churn"},{"name":"churn","model":"other"}`, "two named churn"},
		{`{"name":"churn","model":"say \"hi\""}`, "letters, digits"},
		{`{"name":"churn","model":"churn","memory":"64Mi"}`, "memory"},
		{`{"name":"churn","model":"churn","maxRows":1000000}`, "maxRows"},
	} {
		_, err := Parse([]byte(strings.Replace(spec, "%s", tc.serving, 1)))
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.serving, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want %q", tc.serving, err, tc.want)
		}
	}
	p, _ := Parse([]byte(strings.Replace(spec, "%s", `{"name":"churn","model":"churn"}`, 1)))
	s := *p.ServingOf("churn")
	if p.ServingName(s) != "model-sales-churn" || p.ServingClientID(s) != "serving.sales.churn" || s.Rows() != DefaultServingRows {
		t.Errorf("names: %s %s %d", p.ServingName(s), p.ServingClientID(s), s.Rows())
	}
}
