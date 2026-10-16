package reconcile

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// Postgres is the server that holds Airflow's databases, as its admin: a
// database and a user for each project's Airflow, which can reach its own
// database and no other.
type Postgres struct {
	URL string // postgres://postgres:<password>@airflow-db:5432/postgres
}

// identRE is what may be a database's or a user's name here: the names come
// from projects' (validated) names, and Postgres can't take them as parameters.
var identRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// passwordRE: the passwords the operator makes (hex), so they may be quoted as literals.
var passwordRE = regexp.MustCompile(`^[0-9a-f]{32,128}$`)

func (pg *Postgres) conn(ctx context.Context) (*pgx.Conn, error) {
	c, err := pgx.Connect(ctx, pg.URL)
	if err != nil {
		return nil, fmt.Errorf("Airflow's database server: %w", err) // pgx's errors name the host, not the password
	}
	return c, nil
}

// Ensure makes the user and its database, with this password, and closes
// every database on the server to everyone but its owner (PUBLIC may
// connect to any by default).
func (pg *Postgres) Ensure(ctx context.Context, name, password string) error {
	if !identRE.MatchString(name) || !passwordRE.MatchString(password) {
		return fmt.Errorf("database %q: not a name or password the operator makes", name)
	}
	c, err := pg.conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	var exists bool
	if err := c.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", name).Scan(&exists); err != nil {
		return err
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	if _, err := c.Exec(ctx, fmt.Sprintf(`%s ROLE "%s" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '%s'`, verb, name, password)); err != nil {
		return fmt.Errorf("user %s: %w", name, err)
	}
	if err := c.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := c.Exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s"`, name, name)); err != nil {
			return fmt.Errorf("database %s: %w", name, err)
		}
	}
	return pg.closeToPublic(ctx, c)
}

// closeToPublic takes CONNECT from PUBLIC on every database: each tool's
// user owns its own, and keeps it.
func (pg *Postgres) closeToPublic(ctx context.Context, c *pgx.Conn) error {
	rows, err := c.Query(ctx, "SELECT datname FROM pg_database WHERE NOT datistemplate")
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, db := range names {
		if !identRE.MatchString(db) {
			continue
		}
		if _, err := c.Exec(ctx, fmt.Sprintf(`REVOKE CONNECT, TEMPORARY ON DATABASE "%s" FROM PUBLIC`, db)); err != nil {
			return fmt.Errorf("database %s: %w", db, err)
		}
	}
	return nil
}

// Drop removes a project's database, its runs' history with it, and its user.
func (pg *Postgres) Drop(ctx context.Context, name string) error {
	if !identRE.MatchString(name) {
		return fmt.Errorf("database %q: not a name the operator makes", name)
	}
	c, err := pg.conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, name)); err != nil {
		return fmt.Errorf("database %s: %w", name, err)
	}
	if _, err := c.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, name)); err != nil {
		return fmt.Errorf("user %s: %w", name, err)
	}
	return nil
}
