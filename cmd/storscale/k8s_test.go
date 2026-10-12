package main

import (
	"encoding/json"
	"testing"
)

func TestWhySaysACrashingContainerIsStuck(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
		stuck  bool
	}{
		// Between crashes a container runs ("starting") before it waits
		// in CrashLoopBackOff again: either way, it keeps crashing.
		{`{"name":"setup","restartCount":6,"state":{"running":{}}}`, "starting (6 restarts)", true},
		{`{"name":"setup","restartCount":6,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}`, "CrashLoopBackOff (6 restarts)", true},
		{`{"name":"setup","restartCount":2,"state":{"running":{}}}`, "starting (2 restarts)", false},
		{`{"name":"setup","restartCount":0,"state":{"running":{}}}`, "starting", false},
	} {
		var p pod
		if err := json.Unmarshal([]byte(`{"status":{"phase":"Running","containerStatuses":[`+tc.status+`]}}`), &p); err != nil {
			t.Fatal(err)
		}
		if why, stuck := p.why(); why != tc.want || stuck != tc.stuck {
			t.Errorf("%s: why() = %q, %v; want %q, %v", tc.status, why, stuck, tc.want, tc.stuck)
		}
	}
}
