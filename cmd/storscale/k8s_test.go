package main

import (
	"encoding/json"
	"testing"
	"time"
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

// An image's pull is given longer than other stuck pods: a registry that drops
// connections now and then gets there on a retry.
func TestAPullIsGivenLongerThanACrash(t *testing.T) {
	if stuckFor("api-server: ErrImagePull") != 15*time.Minute || stuckFor("ImagePullBackOff (0 restarts)") != 15*time.Minute {
		t.Error("a pull: want 15 minutes")
	}
	if stuckFor("CrashLoopBackOff (6 restarts)") != 5*time.Minute {
		t.Error("a crash: want 5 minutes")
	}
}
