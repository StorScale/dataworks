package project

import "time"

// Status is what the operator last did with a project, and what came of it.
type Status struct {
	Name     string `json:"name"`
	Phase    string `json:"phase"`    // Ready, Applying, Error, Deleting
	Observed string `json:"observed"` // the Hash of the spec it applied
	// Parts: one per system the project lives in (keycloak, buckets, ranger, nessie).
	Parts   []Part   `json:"parts"`
	Members []Person `json:"members"` // who's in, through which role
	// Reviews are the open merge requests (their iids) whose data branches
	// the operator keeps (use case 7.3).
	Reviews []int     `json:"reviews,omitempty"`
	Updated time.Time `json:"updated"`
	// Spec is the spec the operator applied, so it can undo it once the
	// project is gone.
	Spec *Project `json:"spec,omitempty"`
}

const (
	Ready    = "Ready"
	Applying = "Applying"
	Failed   = "Error"
	Deleting = "Deleting"
)

type Part struct {
	System  string `json:"system"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type Person struct {
	Username string   `json:"username"`
	Role     string   `json:"role"`          // reader, editor, a custom role (16.1), or pipelines, agent, serving
	Via      string   `json:"via"`           // the group they're in it through, or "" when named
	Can      []string `json:"can,omitempty"` // the role's permissions (people's roles alone)
}
