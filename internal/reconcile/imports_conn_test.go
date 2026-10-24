package reconcile

import (
	"strings"
	"testing"

	"github.com/StorScale/storscale-platform/internal/project"
)

// A connector's import hands the runner its sign-in in the connection's Extra.
func TestGongAndHighspotConnections(t *testing.T) {
	p := &project.Project{Metadata: project.Metadata{Name: "sales"}}
	gong := project.Import{Name: "calls", Source: "gong", Connection: "sales-gong"}
	c, err := importConnection(p, gong, map[string]string{"accessKey": "AK", "accessKeySecret": "S", "username": "ignored"})
	if err != nil || c.Type != "generic" || !strings.Contains(c.Extra, `"accessKey":"AK"`) || strings.Contains(c.Extra, "username") {
		t.Errorf("gong: %+v %v", c, err)
	}
	if _, err := importConnection(p, gong, map[string]string{"accessKey": "AK"}); err == nil {
		t.Error("gong without its secret")
	}
	hs := project.Import{Name: "content", Source: "highspot", Connection: "sales-hs"}
	if _, err := importConnection(p, hs, map[string]string{"apiKey": "K", "apiSecret": "S"}); err == nil || !strings.Contains(err.Error(), "baseUrl") {
		t.Errorf("highspot without its address: %v", err)
	}
	c, err = importConnection(p, hs, map[string]string{"apiKey": "K", "apiSecret": "S", "baseUrl": "https://api-su2.highspot.com"})
	if err != nil || !strings.Contains(c.Extra, `"baseUrl":"https://api-su2.highspot.com"`) {
		t.Errorf("highspot: %+v %v", c, err)
	}
}
