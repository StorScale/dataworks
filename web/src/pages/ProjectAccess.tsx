import { Link } from "react-router-dom";
import { getRoles } from "../api";
import { grants, maskLabel, namespaceOf, Spinner, usePlatform, usePoll, useProject } from "../lib";

// ProjectAccess: who's in the project, how, and what each role may do.
export default function ProjectAccess() {
  const { name, view } = useProject();
  const { admin } = usePlatform();
  const roles = usePoll(getRoles, [], 60_000);
  if (!view) return <div className="page"><Spinner /></div>;
  const spec = view.spec?.spec;
  const r = spec?.tables?.readers;
  return (
    <div className="page">
      <div className="grid main-side">
        <div className="stack">
          <section className="card flush">
            <div className="card-head">
              <h2>Members</h2>
              {admin && <Link className="btn small" to="../edit" relative="path">Change members</Link>}
            </div>
            {view.status?.members?.length ? (
              <table className="t" data-testid="members">
                <thead><tr><th>Who</th><th>Role</th><th>Through</th></tr></thead>
                <tbody>
                  {view.status.members.map((m) => (
                    <tr key={m.username}>
                      <td>{m.username}</td>
                      <td><span className={`pill ${m.role === "editor" ? "blue" : ""}`}>{m.role}</span></td>
                      <td className="muted">{m.role === "pipelines" ? "the project's pipelines" : m.via ? `Keycloak group ${m.via}` : "named in the project"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : <p className="muted" style={{ padding: "0 20px 16px" }}>The operator hasn't applied the project yet.</p>}
            {spec && (
              <p className="small muted" style={{ padding: "8px 20px 16px", margin: 0, borderTop: "1px solid var(--line-soft)" }}>
                Members come from {spec.members.map((m) => (m.group ? `group ${m.group}` : `user ${m.user}`) + ` (${m.role})`).join(", ")}.
                Someone who joins one of those groups in Keycloak joins the project within a minute.
              </p>
            )}
          </section>

          {spec && (r?.rowFilters?.length || r?.masks?.length || r?.tables?.length) ? (
            <section className="card flush" data-testid="reader-limits">
              <div className="card-head"><h2>What readers don't see</h2></div>
              <table className="t">
                <thead><tr><th>Table</th><th>Limit</th></tr></thead>
                <tbody>
                  {r?.tables?.length ? <tr><td className="muted">every other table</td><td>Readers read only {r.tables.map((t) => <code key={t}>{t} </code>)}</td></tr> : null}
                  {(r?.rowFilters ?? []).map((f) => <tr key={f.table + f.filter}><td className="mono">{f.table}</td><td>Only rows where <code>{f.filter}</code></td></tr>)}
                  {(r?.masks ?? []).map((m) => <tr key={m.table + m.column}><td className="mono">{m.table}.{m.column}</td><td><span className="pill amber">{maskLabel(m.type)}</span></td></tr>)}
                </tbody>
              </table>
            </section>
          ) : null}
        </div>

        <aside className="stack">
          {spec && grants(name, spec).map((g) => (
            <section key={g.role} className="card" style={g.key === view.role ? { borderColor: "var(--accent)", borderWidth: 2 } : undefined}>
              <div className="card-head" style={{ marginBottom: 6 }}>
                <h2>{g.role}</h2>
                {g.key === view.role ? <span className="pill blue">you</span> : <span className="pill mono" style={{ fontWeight: 400 }}>{g.group}</span>}
              </div>
              <ul className="small" style={{ margin: 0, paddingLeft: 18 }}>{g.items.map((i) => <li key={i}>{i}</li>)}</ul>
            </section>
          ))}
          {/* The roles of the company's own it gives (16.1), in words, as the Roles page defines them. */}
          {spec && roles.data && [...new Set(spec.members.map((m) => m.role))].filter((r) => r !== "editor" && r !== "reader").map((rn) => {
            const def = roles.data!.roles.find((x) => x.name === rn);
            const says = (p: string) => roles.data!.permissions.find((x) => x.name === p)?.says ?? p;
            return (
              <section key={rn} className="card" style={rn === view.role ? { borderColor: "var(--accent)", borderWidth: 2 } : undefined} data-testid={`access-role-${rn}`}>
                <div className="card-head" style={{ marginBottom: 6 }}>
                  <h2>{rn.charAt(0).toUpperCase() + rn.slice(1)}</h2>
                  {rn === view.role ? <span className="pill blue">you</span> : <span className="pill mono" style={{ fontWeight: 400 }}>{name}-role-{rn}</span>}
                </div>
                {def ? <ul className="small" style={{ margin: 0, paddingLeft: 18 }}>{def.granted.map((p) => <li key={p}>{says(p)}</li>)}</ul>
                  : <div className="small" style={{ color: "var(--bad-ink)" }}>No role {rn} is defined: its members have no access until an administrator defines it.</div>}
              </section>
            );
          })}
          {spec && (
            <p className="small muted">
              The platform applies these in Keycloak (groups), Ranger (Trino policies on {namespaceOf(name, spec) ?? "no namespace"}), Buckets and Nessie, and keeps them that way.
            </p>
          )}
        </aside>
      </div>
    </div>
  );
}
