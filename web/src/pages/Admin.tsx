import { useState } from "react";
import { Link, NavLink, Outlet } from "react-router-dom";
import { AccessPerson, getAccess, listProjects } from "../api";
import { ErrorBanner, Phase, Spinner, usePlatform, usePoll } from "../lib";
import { Empty } from "../components";

// Admin is the platform's administration: projects, who has access, monitoring,
// and the administrators' own tools.
export default function Admin() {
  const { tools } = usePlatform();
  const external = tools.filter((t) => t.id === "catalog" || t.section === "admin");
  return (
    <>
      <div className="phead">
        <div className="inner">
          <div className="title"><h1>Administration</h1><span className="muted">Projects, access, the audit log, assistants and the platform's health</span></div>
          <nav className="tabs" aria-label="Administration">
            <NavLink to="/admin/health" data-testid="admin-health">Health</NavLink>
            <NavLink to="/admin/projects" data-testid="admin-projects">Projects</NavLink>
            <NavLink to="/admin/access" data-testid="admin-access">Access</NavLink>
            <NavLink to="/admin/roles" data-testid="admin-roles">Roles</NavLink>
            <NavLink to="/admin/audit" data-testid="admin-audit">Audit</NavLink>
            <NavLink to="/admin/governance" data-testid="admin-governance">Governance</NavLink>
            <NavLink to="/admin/usage" data-testid="admin-usage">Usage</NavLink>
            <NavLink to="/admin/assistants" data-testid="admin-assistants">Assistants</NavLink>
            {tools.some((t) => t.id === "monitoring") && <NavLink to="/admin/monitoring" data-testid="admin-monitoring">Monitoring</NavLink>}
            {external.map((t) => (
              <a key={t.id} href={t.url} target="_blank" rel="noreferrer">{t.name} ↗</a>
            ))}
          </nav>
        </div>
      </div>
      <Outlet />
    </>
  );
}

export function AdminProjects() {
  const { data, error } = usePoll(listProjects, [], 5_000);
  return (
    <div className="page">
      <div className="page-head">
        <p className="lead">
          A project is a team's tables, files and pipelines, and who may use them. The platform applies it to Keycloak, Ranger, Buckets and Nessie, and keeps them that way.
        </p>
        <Link className="btn primary" to="/admin/projects/new" data-testid="new-project">New project</Link>
      </div>
      <ErrorBanner error={error} />
      {!data ? <Spinner /> : data.projects.length === 0 ? <Empty>No projects yet.</Empty> : (
        <div className="card flush">
          <table className="t">
            <thead><tr><th>Project</th><th>Members</th><th>Has</th><th>Status</th></tr></thead>
            <tbody>
              {data.projects.map((v) => (
                <tr key={v.name} data-testid={`project-${v.name}`}>
                  <td><Link to={`/p/${v.name}`}><strong>{v.name}</strong></Link><div className="small muted">{v.spec?.spec.description}</div></td>
                  <td>{v.status?.members.filter((m) => m.role !== "pipelines").length ?? "…"}</td>
                  <td className="small muted">{[v.spec?.spec.tables && "tables", v.spec?.spec.files && "files", v.spec?.spec.pipelines && "pipelines"].filter(Boolean).join(", ")}</td>
                  <td><Phase v={v} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

export function AdminAccess() {
  const { data, error } = usePoll(getAccess, []);
  const [filter, setFilter] = useState("");
  if (error) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  const people = data.people.filter((p: AccessPerson) => p.username.includes(filter.toLowerCase()));
  return (
    <div className="page">
      <div className="page-head">
        <p className="lead">
          Who's in which project, and how. To change someone's access, change the project, or their groups in Keycloak.
        </p>
        <input placeholder="Find a person" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Find a person" />
      </div>
      <div className="card flush scroll-x">
        <table className="t" data-testid="access">
          <thead>
            <tr><th>Person</th>{data.projects.map((p) => <th key={p}><Link to={`/p/${p}/access`}>{p}</Link></th>)}</tr>
          </thead>
          <tbody>
            {people.map((p) => (
              <tr key={p.username}>
                <td>{p.username}</td>
                {data.projects.map((name) => (
                  <td key={name}>
                    {p.roles[name] ? (
                      <span title={p.via[name] ? `through group ${p.via[name]}` : "named in the project"}>
                        <span className={`pill ${p.roles[name] === "editor" ? "blue" : ""}`}>{p.roles[name]}</span>
                        {p.via[name] && <span className="small muted"> via {p.via[name]}</span>}
                      </span>
                    ) : <span className="muted">–</span>}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="grid tiles" style={{ marginTop: 20 }}>
        <div className="card small"><span className="pill blue">editor</span><p>Creates and changes the project's tables and metrics, writes its files, runs its pipelines.</p></div>
        <div className="card small"><span className="pill">reader</span><p>Reads the tables the project shows readers (with its row filters and masks), its files and metrics.</p></div>
        <div className="card small"><span className="pill">pipelines</span><p>A service account: reads and writes the tables, and the files under landing/.</p></div>
      </div>
    </div>
  );
}

// Monitoring is Grafana, in the frame App shows at this address.
export function AdminMonitoring() {
  return null;
}
