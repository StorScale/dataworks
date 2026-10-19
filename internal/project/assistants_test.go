package project

import "testing"

func TestAssistantCheck(t *testing.T) {
	ok := Assistant{Name: "devassist", RedirectURIs: []string{"http://localhost:*", "https://assistant.example.com/callback"}}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	if ok.ClientID() != "assistant-devassist" {
		t.Fatal(ok.ClientID())
	}
	for _, bad := range []Assistant{
		{Name: "Dev Assist", RedirectURIs: []string{"https://a.example.com/cb"}},
		{Name: "devassist"},
		{Name: "devassist", RedirectURIs: []string{"ftp://a.example.com"}},
		{Name: "devassist", RedirectURIs: []string{"http://a.example.com/cb"}},
	} {
		if bad.Check() == nil {
			t.Errorf("%+v passed", bad)
		}
	}
}
