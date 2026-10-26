package reconcile

import (
	"strings"
	"testing"

	"github.com/StorScale/dataworks/internal/project"
)

func TestTrinosGroupsAreTheProjectsLimits(t *testing.T) {
	status := func(name, limits string, members ...project.Person) *project.Status {
		p, err := project.Parse([]byte(`{"metadata": {"name": "` + name + `"}, "spec": {"members": [{"group": "engineers", "role": "editor"}]` + limits + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		return &project.Status{Name: name, Phase: project.Ready, Spec: p, Members: members}
	}
	sales := status("sales", `, "limits": {"queries": 8, "queryMemory": "4GB"}`,
		project.Person{Username: "alice", Role: project.Reader}, project.Person{Username: "bob.smith", Role: project.Editor},
		project.Person{Username: "service-account-project-sales-pipelines", Role: "pipelines"},
		project.Person{Username: "service-account-airflow-pipelines", Role: "pipelines"})
	ads := status("ads", "", project.Person{Username: "bob.smith", Role: project.Editor},
		project.Person{Username: "service-account-airflow-pipelines", Role: "pipelines"})
	gone := status("old", "")
	gone.Phase = project.Deleting

	tl := &TrinoLimits{Environment: "docker", Project: project.Limits{Queries: 4, Queued: 40, QueryMemory: "50%"}, Person: project.Limits{Queries: 2, Queued: 20}}
	groups, selectors := tl.layout([]*project.Status{sales, gone, ads})

	var names []string
	for _, g := range groups {
		names = append(names, g.name)
	}
	if strings.Join(names, ",") != "global,people,${USER},project-ads,project-sales" {
		t.Fatalf("groups %v: the install's, a person's, then each project's, sorted; none for one being deleted", names)
	}
	if s := groups[4]; s.hard != 8 || s.queued != 40 || s.memory != "4GB" || s.parent != 0 {
		t.Errorf("sales: %+v, want its own limits, the install's for the rest", s)
	}
	if a := groups[3]; a.hard != 4 || a.memory != "50%" {
		t.Errorf("ads: %+v, want the install's", a)
	}
	got := map[string]trinoSelector{}
	for _, s := range selectors {
		got[groups[s.group].name+"|"+strings.Join(s.tags, ",")] = s
	}
	// A pipelines account shared by several projects (Compose's) is as a person: its
	// queries count against a project when they name it.
	if s := got["project-sales|project:sales"]; s.user != `^(alice|bob\.smith|service-account-airflow-pipelines)$` || s.priority != 100 {
		t.Errorf("sales' members, naming sales: %+v (a username is a regex, quoted)", s)
	}
	if s := got["project-sales|"]; s.user != `^(service-account-project-sales-pipelines)$` || s.priority != 200 {
		t.Errorf("sales' own pipelines account, whatever it names: %+v (not the shared one, which is in ads too)", s)
	}
	if _, ok := got["project-ads|"]; ok {
		t.Error("ads has no pipelines account of its own: no selector for it")
	}
	if s := got["${USER}|"]; s.user != "" || s.priority != 1 {
		t.Errorf("everything else is its person's: %+v", s)
	}
}
