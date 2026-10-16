import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { getProject, MASK_TYPES, Member, Project, putProject, Spec } from "../api";
import { grants } from "../lib";

const blank = (): Project => ({
  apiVersion: "platform.storscale.io/v1alpha1",
  kind: "Project",
  metadata: { name: "" },
  spec: { description: "", members: [{ group: "engineers", role: "editor" }], tables: {}, files: {} },
});

const list = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);

// ProjectEditor makes or changes a project, as a form or as its JSON.
export default function ProjectEditor() {
  const { project: name } = useParams();
  const navigate = useNavigate();
  const [p, setP] = useState<Project>();
  const [json, setJson] = useState<string>();
  const [error, setError] = useState<string>();
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!name) setP(blank());
    else getProject(name).then((v) => v.spec && setP(v.spec)).catch((e) => setError(e.message));
  }, [name]);
  if (!p) return error ? <p className="bad">{error}</p> : <div className="spinner" />;

  const spec = p.spec;
  const set = (s: Partial<Spec>) => setP({ ...p, spec: { ...spec, ...s } });
  const setMember = (i: number, m: Partial<Member>) => set({ members: spec.members.map((x, j) => (j === i ? { ...x, ...m } : x)) });
  const readers = spec.tables?.readers ?? {};
  const setReaders = (r: typeof readers) => set({ tables: { ...spec.tables, readers: r } });
  const setNotify = (n: Partial<NonNullable<Spec["notify"]>>) => {
    const next = { ...spec.notify, ...n };
    set({ notify: next.teams || next.email?.length ? next : undefined });
  };
  const setFreshness = (i: number, f: Partial<{ table: string; within: string }>) =>
    set({ tables: { ...spec.tables, freshness: spec.tables!.freshness!.map((x, j) => (j === i ? { ...x, ...f } : x)) } });

  const save = async () => {
    setSaving(true);
    setError(undefined);
    try {
      const body = json !== undefined ? (JSON.parse(json) as Project) : p;
      await putProject(body);
      navigate(`/p/${body.metadata.name}`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="page">
      <div className="page-head">
        <h1>{name ? "Edit project" : "New project"}</h1>
        <div className="segmented">
          <button className={json === undefined ? "selected" : ""} onClick={() => setJson(undefined)} disabled={json !== undefined && !validJSON(json)}>Form</button>
          <button className={json !== undefined ? "selected" : ""} onClick={() => setJson(JSON.stringify(p, null, 2))}>JSON</button>
        </div>
      </div>
      {error && <div className="banner error" data-testid="editor-error">{error}</div>}

      {json !== undefined ? (
        <textarea className="yaml" rows={28} value={json} onChange={(e) => { setJson(e.target.value); if (validJSON(e.target.value)) setP(JSON.parse(e.target.value)); }} spellCheck={false} data-testid="project-json" />
      ) : (
        <div className="form card">
          <label>Name
            <input value={p.metadata.name} disabled={!!name} placeholder="sales" data-testid="project-name"
              onChange={(e) => setP({ ...p, metadata: { name: e.target.value.toLowerCase() } })} />
            <span className="field-help">Lowercase letters, digits and dashes. Its groups, bucket and namespace are named after it.</span>
          </label>
          <label>Description
            <input value={spec.description ?? ""} onChange={(e) => set({ description: e.target.value })} />
          </label>

          <h2 style={{ marginTop: 8 }}>Members</h2>
          {spec.members.map((m, i) => (
            <div className="row" key={i}>
              <select value={m.user !== undefined ? "user" : "group"} onChange={(e) =>
                setMember(i, e.target.value === "user" ? { user: m.group ?? "", group: undefined } : { group: m.user ?? "", user: undefined })}>
                <option value="group">Keycloak group</option>
                <option value="user">Person</option>
              </select>
              <input value={m.group ?? m.user ?? ""} placeholder={m.user !== undefined ? "username" : "group"}
                onChange={(e) => setMember(i, m.user !== undefined ? { user: e.target.value } : { group: e.target.value })} />
              <select value={m.role} onChange={(e) => setMember(i, { role: e.target.value as Member["role"] })}>
                <option value="editor">editor</option>
                <option value="reader">reader</option>
              </select>
              <button className="linkish" onClick={() => set({ members: spec.members.filter((_, j) => j !== i) })}>Remove</button>
            </div>
          ))}
          <button className="btn" onClick={() => set({ members: [...spec.members, { group: "", role: "reader" }] })}>Add a member</button>

          <h2 style={{ marginTop: 20 }}>
            <label className="row" style={{ flexDirection: "row", gap: 8 }}><input type="checkbox" checked={!!spec.tables} onChange={(e) => set({ tables: e.target.checked ? {} : undefined })} /> Tables</label>
          </h2>
          {spec.tables && (
            <>
              <label>Namespace
                <input value={spec.tables.namespace ?? ""} placeholder={(p.metadata.name || "name").replace(/-/g, "_")}
                  onChange={(e) => set({ tables: { ...spec.tables, namespace: e.target.value || undefined } })} />
                <span className="field-help">In the iceberg catalog (Nessie), queried through Trino.</span>
              </label>
              <label>Tables readers may read
                <input value={(readers.tables ?? []).join(", ")} placeholder="all of them"
                  onChange={(e) => setReaders({ ...readers, tables: list(e.target.value) })} />
              </label>
              <h3>Row filters for readers</h3>
              {(readers.rowFilters ?? []).map((f, i) => (
                <div className="row" key={i}>
                  <input value={f.table} placeholder="table" onChange={(e) => setReaders({ ...readers, rowFilters: readers.rowFilters!.map((x, j) => (j === i ? { ...x, table: e.target.value } : x)) })} />
                  <input className="grow" value={f.filter} placeholder="region = 'EU'" onChange={(e) => setReaders({ ...readers, rowFilters: readers.rowFilters!.map((x, j) => (j === i ? { ...x, filter: e.target.value } : x)) })} />
                  <button className="linkish" onClick={() => setReaders({ ...readers, rowFilters: readers.rowFilters!.filter((_, j) => j !== i) })}>Remove</button>
                </div>
              ))}
              <button className="btn" onClick={() => setReaders({ ...readers, rowFilters: [...(readers.rowFilters ?? []), { table: "", filter: "" }] })}>Add a row filter</button>
              <h3>Column masks for readers</h3>
              {(readers.masks ?? []).map((m, i) => (
                <div className="row" key={i}>
                  <input value={m.table} placeholder="table" onChange={(e) => setReaders({ ...readers, masks: readers.masks!.map((x, j) => (j === i ? { ...x, table: e.target.value } : x)) })} />
                  <input value={m.column} placeholder="column" onChange={(e) => setReaders({ ...readers, masks: readers.masks!.map((x, j) => (j === i ? { ...x, column: e.target.value } : x)) })} />
                  <select value={m.type} onChange={(e) => setReaders({ ...readers, masks: readers.masks!.map((x, j) => (j === i ? { ...x, type: e.target.value } : x)) })}>
                    {MASK_TYPES.map((t) => <option key={t}>{t}</option>)}
                  </select>
                  <button className="linkish" onClick={() => setReaders({ ...readers, masks: readers.masks!.filter((_, j) => j !== i) })}>Remove</button>
                </div>
              ))}
              <button className="btn" onClick={() => setReaders({ ...readers, masks: [...(readers.masks ?? []), { table: "", column: "", type: "MASK" }] })}>Add a mask</button>
            </>
          )}

          <h2 style={{ marginTop: 20 }}>
            <label className="row" style={{ flexDirection: "row", gap: 8 }}><input type="checkbox" checked={!!spec.files} onChange={(e) => set({ files: e.target.checked ? {} : undefined })} /> Files</label>
          </h2>
          {spec.files && (
            <label>Bucket
              <input value={spec.files.bucket ?? ""} placeholder={p.metadata.name || "name"} onChange={(e) => set({ files: { bucket: e.target.value || undefined } })} />
            </label>
          )}

          <h2 style={{ marginTop: 20 }}>
            <label className="row" style={{ flexDirection: "row", gap: 8 }}><input type="checkbox" checked={!!spec.pipelines} onChange={(e) => set({ pipelines: e.target.checked ? { serviceAccount: "" } : undefined })} /> Pipelines</label>
          </h2>
          {spec.pipelines && (
            <label>Keycloak client whose service account runs them
              <input value={spec.pipelines.serviceAccount} placeholder="airflow-pipelines" onChange={(e) => set({ pipelines: { ...spec.pipelines!, serviceAccount: e.target.value } })} />
            </label>
          )}

          <h2 style={{ marginTop: 20 }}>Alerts</h2>
          <p className="muted small" style={{ margin: "4px 0 8px" }}>
            Where the project is told when a pipeline's run fails, a check fails, or a table isn't changed in time.
          </p>
          <label>Teams channel: the name of a connection of the project's, holding the channel's webhook
            <input value={spec.notify?.teams ?? ""} placeholder="alerts" onChange={(e) => setNotify({ teams: e.target.value || undefined })} />
          </label>
          <label>Email
            <input value={(spec.notify?.email ?? []).join(", ")} placeholder="sales-data@example.com" onChange={(e) => setNotify({ email: list(e.target.value).length ? list(e.target.value) : undefined })} />
          </label>
          {spec.tables && (
            <>
              <div className="small" style={{ marginTop: 8 }}>Tables that must change in time</div>
              {(spec.tables.freshness ?? []).map((f, i) => (
                <div key={i} className="row">
                  <input value={f.table} placeholder="table" onChange={(e) => setFreshness(i, { table: e.target.value })} />
                  <input value={f.within} placeholder="24h" style={{ width: 90 }} onChange={(e) => setFreshness(i, { within: e.target.value })} />
                  <button className="btn" onClick={() => set({ tables: { ...spec.tables, freshness: spec.tables!.freshness!.filter((_, j) => j !== i) } })}>Remove</button>
                </div>
              ))}
              <button className="btn" onClick={() => set({ tables: { ...spec.tables, freshness: [...(spec.tables!.freshness ?? []), { table: "", within: "24h" }] } })}>Add a table</button>
            </>
          )}
        </div>
      )}

      {p.metadata.name && (
        <>
          <h2 style={{ margin: "24px 0 12px" }}>What each role will get</h2>
          <div className="grid cards">
            {grants(p.metadata.name, p.spec).map((g) => (
              <div key={g.role} className="card"><h3>{g.role} <span className="pill">{g.group}</span></h3><ul>{g.items.map((i) => <li key={i}>{i}</li>)}</ul></div>
            ))}
          </div>
        </>
      )}
      <div className="form-actions">
        <button className="btn" onClick={() => navigate(-1)}>Cancel</button>
        <button className="btn primary" onClick={save} disabled={saving} data-testid="save-project">{saving ? "Saving…" : "Save"}</button>
      </div>
    </div>
  );
}

function validJSON(s: string) {
  try {
    JSON.parse(s);
    return true;
  } catch {
    return false;
  }
}
