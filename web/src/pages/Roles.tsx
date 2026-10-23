import { useState } from "react";
import { deleteRole, getRoles, putRole, type Permission, type Role } from "../api";
import { Empty, Intro } from "../components";
import { ErrorBanner, Spinner, usePoll } from "../lib";

// Roles of the company's own (use case 16.1): project roles (editor and
// reader built in, as the platform's permissions, and the ones the
// administrators define from them), which a project gives to people and
// groups; and platform roles (finance, governance and data protection, from
// the install's settings, and the defined ones), given here to Keycloak
// groups across every project. A change reaches everyone it covers within a
// minute; a project role a project gives can't be removed.
export default function AdminRoles() {
  const { data, error, reload } = usePoll(getRoles, [], 30_000);
  const [editing, setEditing] = useState<Role | "project" | "platform">();
  const [actionError, setActionError] = useState<string>();
  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  const says = (p: string) => [...data.permissions, ...data.platformPermissions].find((x) => x.name === p)?.says ?? p;
  const remove = async (r: Role) => {
    if (!confirm(`Remove the role ${r.name}?`)) return;
    setActionError(undefined);
    try { await deleteRole(r.name); reload(); } catch (e) { setActionError((e as Error).message); }
  };
  const projectRoles = data.roles.filter((r) => !r.scope), platformRoles = data.roles.filter((r) => r.scope === "platform");
  const card = (r: Role) => (
    <div key={r.name} className="card stack" style={{ gap: 8 }} data-testid={`role-${r.name}`}>
      <div className="card-head">
        <h2>{r.name}{r.builtIn && <span className="pill">{r.scope === "platform" ? "the install's" : "built in"}</span>}</h2>
        {data.canEdit && !r.builtIn && (
          <div className="row">
            <button className="btn small" onClick={() => setEditing(r)}>Change</button>
            <button className="btn small danger" disabled={(r.projects ?? []).length > 0} title={(r.projects ?? []).length ? `Given in ${(r.projects ?? []).join(", ")}` : undefined}
              onClick={() => remove(r)}>Remove</button>
          </div>
        )}
      </div>
      {r.description && <div className="small">{r.description}</div>}
      <ul className="small" style={{ margin: 0, paddingLeft: 18 }}>
        {(r.granted ?? r.permissions ?? []).map((p) => <li key={p}>{says(p)}{!(r.permissions ?? []).includes(p) && <span className="muted"> (comes with another)</span>}</li>)}
      </ul>
      <div className="small muted">
        {r.scope === "platform"
          ? <>Given to the Keycloak groups <span className="mono">{(r.groups ?? []).join(", ") || "none"}</span>{r.builtIn ? " (the install's settings)" : ""}.</>
          : (r.projects ?? []).length ? `Given in ${(r.projects ?? []).join(", ")}.` : "No project gives it."}
        {r.changedBy && ` Changed by ${r.changedBy}${r.changedAt ? ", " + new Date(r.changedAt).toLocaleString() : ""}.`}
      </div>
    </div>
  );
  return (
    <div className="page stack" data-testid="roles">
      <Intro actions={data.canEdit && !editing && <div className="row">
        <button className="btn primary" onClick={() => setEditing("project")} data-testid="add-role">Define a project role</button>
        <button className="btn" onClick={() => setEditing("platform")} data-testid="add-platform-role">Define a platform role</button></div>}>
        What people may do, as the platform's permissions. A project role (editor and reader are built in) is given by a project to people and
        groups on its Edit page; a platform role is given here to Keycloak groups, across every project. A change reaches everyone it covers
        within a minute, in Trino, Buckets, Airflow and the platform; each is in the audit log.
      </Intro>
      <ErrorBanner error={actionError} />
      {editing && <RoleForm role={typeof editing === "string" ? undefined : editing} scope={typeof editing === "string" ? editing : editing.scope === "platform" ? "platform" : "project"}
        permissions={(typeof editing === "string" ? editing : editing.scope === "platform" ? "platform" : "project") === "platform" ? data.platformPermissions : data.permissions}
        onDone={() => { setEditing(undefined); reload(); }} />}
      <h2 style={{ marginTop: 8 }}>Project roles</h2>
      {projectRoles.filter((r) => !r.builtIn).length === 0 && !editing && <Empty inline>No project roles of the company's own yet: only editor and reader.</Empty>}
      <div className="grid cards">{projectRoles.map(card)}</div>
      <h2 style={{ marginTop: 8 }}>Platform roles</h2>
      <div className="grid cards">{platformRoles.map(card)}</div>
    </div>
  );
}

function RoleForm({ role, scope, permissions, onDone }: { role?: Role; scope: "project" | "platform"; permissions: Permission[]; onDone: () => void }) {
  const [name, setName] = useState(role?.name ?? "");
  const [description, setDescription] = useState(role?.description ?? "");
  const [picked, setPicked] = useState<string[]>(role?.permissions ?? (scope === "project" ? ["read"] : []));
  const [groups, setGroups] = useState((role?.groups ?? []).join(", "));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const groupList = groups.split(",").map((g) => g.trim()).filter(Boolean);
  const save = async () => {
    setBusy(true); setError(undefined);
    try {
      await putRole(name, { description: description || undefined, permissions: picked, scope: scope === "platform" ? "platform" : undefined,
        groups: scope === "platform" ? groupList : undefined });
      onDone();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <form className="card stack" onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="role-form">
      <h2>{role ? `Change ${role.name}` : scope === "platform" ? "Define a platform role" : "Define a project role"}</h2>
      <ErrorBanner error={error} />
      <div className="fields">
        <label>Name<input className="mono" value={name} disabled={!!role} onChange={(e) => setName(e.target.value)} placeholder={scope === "platform" ? "privacy" : "steward"} data-testid="role-name" /></label>
        <label className="grow">What it's for<input value={description} onChange={(e) => setDescription(e.target.value)}
          placeholder={scope === "platform" ? "The privacy office: approves deliveries of personal data, answers erasure requests" : "Reads every table as it is and confirms the catalog's tags; changes nothing"} /></label>
      </div>
      {scope === "platform" && <label>Keycloak groups given it<input className="mono" value={groups} onChange={(e) => setGroups(e.target.value)} placeholder="privacy-office" data-testid="role-groups" />
        <span className="field-help">Comma-separated: everyone in them has it, across every project.</span></label>}
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
        <button className="btn primary" type="submit" disabled={busy || !name || picked.length === 0 || (scope === "platform" && groupList.length === 0)} data-testid="save-role">
          {busy ? "Saving…" : role ? "Save" : "Define it"}</button>
        <button className="btn" type="button" onClick={onDone}>Cancel</button>
        <span className="field-help">{scope === "platform" ? "Everyone in its groups has it within a minute." : "Every project giving it follows within a minute."}</span>
      </div>
    </form>
  );
}
