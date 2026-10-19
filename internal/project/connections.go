package project

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The kinds of connection a project's Connections tab adds (use case 7.6),
// each with the keys the platform reads from it. A secret key's value is
// never shown again; the others' are (a host, a user's name, a bucket).

type ConnectionField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
	Multi    bool   `json:"multiline,omitempty"` // a key, a certificate
}

type ConnectionKind struct {
	Kind   string            `json:"kind"`
	Label  string            `json:"label"`
	For    string            `json:"for"` // where a project names it
	Fields []ConnectionField `json:"fields"`
	// OneOf: at least one of these keys has a value.
	OneOf []string `json:"oneOf,omitempty"`
}

var ConnectionKinds = []ConnectionKind{
	{Kind: "git", Label: "A git token", For: "the pipelines' repo (https)", Fields: []ConnectionField{
		{Key: "username", Label: "User"}, {Key: "password", Label: "Token", Secret: true, Required: true},
		{Key: "webhookToken", Label: "GitLab webhook's token", Secret: true}}},
	{Kind: "ssh", Label: "An SSH key", For: "the pipelines' repo (ssh)", Fields: []ConnectionField{
		{Key: "sshPrivateKey", Label: "Private key", Secret: true, Required: true, Multi: true},
		{Key: "sshPrivateKeyPassphrase", Label: "Its passphrase", Secret: true}}},
	{Kind: "database", Label: "A database user", For: "sources, copies and sql_database imports", Fields: []ConnectionField{
		{Key: "host", Label: "Host"}, {Key: "port", Label: "Port"}, {Key: "database", Label: "Database"},
		{Key: "username", Label: "User", Required: true}, {Key: "password", Label: "Password", Secret: true, Required: true},
		{Key: "url", Label: "JDBC URL (in place of host, port and database)"}, {Key: "sslmode", Label: "sslmode (PostgreSQL)"},
		{Key: "trustServerCertificate", Label: "trustServerCertificate (SQL Server: true)"}}, OneOf: []string{"host", "url"}},
	{Kind: "salesforce", Label: "A Salesforce org", For: "Salesforce imports (8.4)", Fields: []ConnectionField{
		{Key: "username", Label: "User"}, {Key: "password", Label: "Password", Secret: true},
		{Key: "securityToken", Label: "Security token", Secret: true},
		{Key: "consumerKey", Label: "Connected app's consumer key", Secret: true}, {Key: "consumerSecret", Label: "Connected app's consumer secret", Secret: true},
		{Key: "organizationId", Label: "Organization ID (sign-in from trusted IP ranges)"},
		{Key: "domain", Label: "Domain: login (production) or test (a sandbox)"},
		{Key: "instanceUrl", Label: "Instance address (with a session, in place of a user)"}, {Key: "sessionId", Label: "Session", Secret: true},
		{Key: "caCertificate", Label: "A CA's certificate to trust as well (a proxy that inspects TLS)", Multi: true}},
		OneOf: []string{"password", "sessionId"}},
	{Kind: "api", Label: "An API token", For: "rest_api imports", Fields: []ConnectionField{
		{Key: "token", Label: "Token", Secret: true, Required: true}}},
	{Kind: "sftp", Label: "An SFTP server", For: "deliveries' targets", Fields: []ConnectionField{
		{Key: "host", Label: "Host", Required: true}, {Key: "port", Label: "Port"}, {Key: "username", Label: "User", Required: true},
		{Key: "password", Label: "Password", Secret: true}, {Key: "sshPrivateKey", Label: "Private key", Secret: true, Multi: true},
		{Key: "hostKey", Label: "The server's host key (as in known_hosts)", Required: true}, {Key: "path", Label: "Folder"}},
		OneOf: []string{"password", "sshPrivateKey"}},
	{Kind: "s3", Label: "An S3 bucket", For: "deliveries' targets", Fields: []ConnectionField{
		{Key: "endpoint", Label: "Endpoint (empty: AWS)"}, {Key: "region", Label: "Region"}, {Key: "bucket", Label: "Bucket", Required: true},
		{Key: "prefix", Label: "Prefix"}, {Key: "accessKey", Label: "Access key", Required: true},
		{Key: "secretKey", Label: "Secret key", Secret: true, Required: true}}},
	{Kind: "pgp", Label: "A recipient's PGP key", For: "deliveries' encryption", Fields: []ConnectionField{
		{Key: "pgpPublicKey", Label: "Public key (armored)", Required: true, Multi: true}}},
	{Kind: "agent", Label: "An agent's secret", For: "an agent of the project's (12.2; made when it's approved)", Fields: []ConnectionField{
		{Key: "clientId", Label: "Client"}, {Key: "secret", Label: "Secret", Secret: true, Required: true}}},
	{Kind: "teams", Label: "A Teams channel", For: "the project's alerts (notify.teams)", Fields: []ConnectionField{
		{Key: "webhook", Label: "Workflows webhook URL", Secret: true, Required: true}}},
}

// KindOf is the kind named, or nil.
func KindOf(kind string) *ConnectionKind {
	i := slices.IndexFunc(ConnectionKinds, func(k ConnectionKind) bool { return k.Kind == kind })
	if i < 0 {
		return nil
	}
	return &ConnectionKinds[i]
}

var connectionNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ConnectionNameProblem is what's wrong with a new connection's name: the
// project's name and a dash first (sales-dags), so it can't be the
// platform's or another project's.
func ConnectionNameProblem(project, name string) string {
	if !connectionNameRE.MatchString(name) || !strings.HasPrefix(name, project+"-") || name == project+"-" {
		return fmt.Sprintf("a connection's name starts with the project's and a dash (%s-dags), in lower-case letters, digits and dashes", project)
	}
	return ""
}

// ConnectionValuesProblems are what's wrong with a connection's values for its kind.
func (k *ConnectionKind) ValuesProblems(values map[string]string) []string {
	var out []string
	for key, v := range values {
		if !slices.ContainsFunc(k.Fields, func(f ConnectionField) bool { return f.Key == key }) {
			out = append(out, fmt.Sprintf("%s: no such key for %s", key, k.Label))
		} else if len(v) > 64<<10 {
			out = append(out, key+": too long")
		}
	}
	for _, f := range k.Fields {
		if f.Required && strings.TrimSpace(values[f.Key]) == "" {
			out = append(out, f.Label+" is needed")
		}
	}
	if len(k.OneOf) > 0 && !slices.ContainsFunc(k.OneOf, func(key string) bool { return strings.TrimSpace(values[key]) != "" }) {
		out = append(out, "one of "+strings.Join(k.OneOf, ", ")+" is needed")
	}
	slices.Sort(out)
	return out
}

// ConnectionUses are where a project's spec names a connection.
func (p *Project) ConnectionUses(name string) []string {
	var out []string
	s := p.Spec
	if s.Pipelines != nil && s.Pipelines.Credentials != nil && s.Pipelines.Credentials.SecretRef == name {
		out = append(out, "the pipelines' repo")
	}
	if s.Notify != nil && s.Notify.Teams == name {
		out = append(out, "alerts (notify.teams)")
	}
	for _, src := range s.Sources {
		if src.Connection == name {
			out = append(out, "source "+src.Name)
		}
	}
	for _, im := range s.Imports {
		if im.Connection == name {
			out = append(out, "import "+im.Name)
		}
	}
	for _, d := range s.Deliveries {
		if d.Target == name || d.PGP == name {
			out = append(out, "delivery "+d.Name)
		}
	}
	return out
}

// ValidCron says whether a schedule is cron's five fields.
func ValidCron(s string) bool { return cronRE.MatchString(s) }
