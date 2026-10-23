import { useState } from "react";
import { deleteRole, getRoles, putRole, type Permission, type Role } from "../api";
import { Empty, Intro } from "../components";
import { ErrorBanner, Spinner, usePoll } from "../lib";

// Roles of the company's own (use case 16.1): editor and reader, built in, as
// the platform's permissions; and the roles the administrators define from
// the same permissions, which a project gives to people and groups as it
// gives editor and reader. A change reaches every project giving the role
// within a minute; a role a project gives can't be removed.
export default function AdminRoles() {
  const { data, error, reload } = usePoll(getRoles, [], 30_000);
  const [editing, setEditing] = useState<Role | "new">();
  const [actionError, setActionError] = useState<string>();
  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  const says = (p: string) => data.permissions.find((x) => x.name === p)?.says ?? p;
  const remove = async (r: Role) => {
    if (!confirm(`Remove the role ${r.name}?`)) return;
    setActionError(undefined);
    try { await deleteRole(r.name); reload(); } catch (e) { setActionError((e as Error).message); }
  };
  return (
    <div className="page stack" data-testid="roles">
      <Intro actions={data.canEdit && !editing && <button className="btn primary" onClick={() => setEditing("new")} data-testid="add-role">Define a role</button>}>
        What people may do in a project, as the platform's permissions: editor and reader are built in; a role of the company's own (a steward,
        an analyst, an ML engineer) is defined here, and a project gives it to people and groups on its Edit page. A change reaches every project
        giving it within a minute, in Trino, Buckets, Airflow and the platform.
      </Intro>
      <ErrorBanner error={actionError} />
      {editing && <RoleForm role={editing === "new" ? undefined : editing} permissions={data.permissions}
        onDone={() => { setEditing(undefined); reload(); }} />}
      {data.roles.filter((r) => !r.builtIn).length === 0 && !editing && <Empty>No roles of the company's own yet: only editor and reader.</Empty>}
      <div className="grid cards">
        {data.roles.map((r) => (
          <div key={r.name} className="card stack" style={{ gap: 8 }} data-testid={`role-${r.name}`}>
            <div className="card-head">
              <h2>{r.name}{r.builtIn && <span className="pill">built in</span>}</h2>
              {data.canEdit && !r.builtIn && (
                <div className="row">
                  <button className="btn small" onClick={() => setEditing(r)}>Change</button>
                  <button className="btn small danger" disabled={r.projects.length > 0} title={r.projects.length ? `Given in ${r.projects.join(", ")}` : undefined}
                    onClick={() => remove(r)}>Remove</button>
                </div>
              )}
            </div>
            {r.description && <div className="small">{r.description}</div>}
            <ul className="small" style={{ margin: 0, paddingLeft: 18 }}>
              {r.granted.map((p) => <li key={p}>{says(p)}{!r.permissions.includes(p) && <span className="muted"> (comes with another)</span>}</li>)}
            </ul>
            <div className="small muted">
              {r.projects.length ? `Given in ${r.projects.join(", ")}.` : "No project gives it."}
              {r.changedBy && ` Changed by ${r.changedBy}${r.changedAt ? ", " + new Date(r.changedAt).toLocaleString() : ""}.`}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function RoleForm({ role, permissions, onDone }: { role?: Role; permissions: Permission[]; onDone: () => void }) {
  const [name, setName] = useState(role?.name ?? "");
  const [description, setDescription] = useState(role?.description ?? "");
  const [picked, setPicked] = useState<string[]>(role?.permissions ?? ["read"]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const save = async () => {
    setBusy(true); setError(undefined);
    try { await putRole(name, { description: description || undefined, permissions: picked }); onDone(); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <form className="card stack" onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="role-form">
      <h2>{role ? `Change ${role.name}` : "Define a role"}</h2>
      <ErrorBanner error={error} />
      <div className="fields">
        <label>Name<input className="mono" value={name} disabled={!!role} onChange={(e) => setName(e.target.value)} placeholder="steward" data-testid="role-name" /></label>
        <label className="grow">What it's for<input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Reads every table as it is and confirms the catalog's tags; changes nothing" /></label>
      </div>
      <div className="stack" style={{ gap: 8 }}>
        <span className="field-label">What it may do</span>
        <div className="stack" style={{ gap: 6 }}>
          {permissions.map((p) => (
            <label key={p.name} className="check">
              <input type="checkbox" checked={picked.includes(p.name)} data-testid={`perm-${p.name}`}
                onChange={(e) => setPicked(e.target.checked ? [...picked, p.name] : picked.filter((x) => x !== p.name))} />
              <span><span className="mono small">{p.name}</span> <span className="muted">{p.says}</span></span>
            </label>
          ))}
        </div>
      </div>
      <div className="form-actions">
        <button className="btn primary" type="submit" disabled={busy || !name || picked.length === 0} data-testid="save-role">{busy ? "Saving…" : role ? "Save" : "Define it"}</button>
        <button className="btn" type="button" onClick={onDone}>Cancel</button>
        <span className="field-help">Every project giving it follows within a minute.</span>
      </div>
    </form>
  );
}
