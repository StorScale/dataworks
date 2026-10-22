import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { APIResource, getProject, resetImport, Import, MASK_TYPES, Member, Project, putProject, Source, sourceCatalog, Spec } from "../api";
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
  const [resetDone, setResetDone] = useState<string>();
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
  const setImport = (i: number, x: Partial<Import>) => set({ imports: spec.imports!.map((y, j) => (j === i ? { ...y, ...x } : y)) });
  const setResource = (i: number, k: number, r: Partial<APIResource>) => {
    const im = spec.imports![i];
    setImport(i, { api: { ...im.api!, resources: im.api!.resources.map((y, j) => (j === k ? { ...y, ...r } : y)) } });
  };
  const setDBTable = (i: number, k: number, t: Partial<NonNullable<Import["database"]>["tables"][number]>) => {
    const im = spec.imports![i];
    setImport(i, { database: { ...im.database!, tables: im.database!.tables.map((y, j) => (j === k ? { ...y, ...t } : y)) } });
  };
  const setSource = (i: number, s: Partial<Source>) => set({ sources: spec.sources!.map((x, j) => (j === i ? { ...x, ...s } : x)) });
  const setNotify = (n: Partial<NonNullable<Spec["notify"]>>) => {
    const next = { ...spec.notify, ...n };
    set({ notify: next.teams || next.slack || next.email?.length ? next : undefined });
  };
  const setMaintenance = (m: Partial<NonNullable<NonNullable<Spec["tables"]>["maintenance"]>>) => {
    const next = { ...spec.tables?.maintenance, ...m };
    const kept = Object.values(next).some((v) => v !== undefined);
    set({ tables: { ...spec.tables, maintenance: kept ? next : undefined } });
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

          <h2>Members</h2>
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
              <button className="btn" onClick={() => set({ members: spec.members.filter((_, j) => j !== i) })}>Remove</button>
            </div>
          ))}
          <button className="btn" onClick={() => set({ members: [...spec.members, { group: "", role: "reader" }] })}>Add a member</button>

          <h2>
            <label className="check"><input type="checkbox" checked={!!spec.tables} onChange={(e) => set({ tables: e.target.checked ? {} : undefined })} />Tables</label>
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
                  <button className="btn" onClick={() => setReaders({ ...readers, rowFilters: readers.rowFilters!.filter((_, j) => j !== i) })}>Remove</button>
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
                  <button className="btn" onClick={() => setReaders({ ...readers, masks: readers.masks!.filter((_, j) => j !== i) })}>Remove</button>
                </div>
              ))}
              <button className="btn" onClick={() => setReaders({ ...readers, masks: [...(readers.masks ?? []), { table: "", column: "", type: "MASK" }] })}>Add a mask</button>
            </>
          )}

          <h2>
            <label className="check"><input type="checkbox" checked={!!spec.files} onChange={(e) => set({ files: e.target.checked ? {} : undefined })} />Files</label>
          </h2>
          {spec.files && (
            <label>Bucket
              <input value={spec.files.bucket ?? ""} placeholder={p.metadata.name || "name"} onChange={(e) => set({ files: { bucket: e.target.value || undefined } })} />
            </label>
          )}

          <h2>
            <label className="check"><input type="checkbox" checked={!!spec.pipelines} onChange={(e) => set({ pipelines: e.target.checked ? { serviceAccount: "" } : undefined })} />Pipelines</label>
          </h2>
          {spec.pipelines && (() => {
            const pl = spec.pipelines!;
            const setPl = (patch: Partial<NonNullable<Spec["pipelines"]>>) => set({ pipelines: { ...pl, ...patch } });
            const setReviews = (patch: Partial<NonNullable<NonNullable<Spec["pipelines"]>["reviews"]>>) => {
              const rv = { ...(pl.reviews ?? {}), ...patch };
              Object.keys(rv).forEach((k) => { if (!rv[k as keyof typeof rv]) delete rv[k as keyof typeof rv]; });
              setPl({ reviews: Object.keys(rv).length ? rv : undefined });
            };
            return (
              <div className="stack" style={{ gap: 4 }} data-testid="pipelines-form">
                <label>Keycloak client whose service account runs them
                  <input value={pl.serviceAccount} placeholder="airflow-pipelines" onChange={(e) => setPl({ serviceAccount: e.target.value })} />
                </label>
                <p className="field-help">
                  Their git repo (use case 7.1): the DAGs come from it, and on Kubernetes the project gets an Airflow of its own. A private repo signs in with
                  one of the project's connections (a git token or an SSH key, given on its Connections tab; a GitLab token needs the api scope and the Developer role). The canvas saves its flows there.
                </p>
                <label>Repo (https or ssh address, no credentials in it)
                  <input className="mono" value={pl.repo ?? ""} placeholder="https://gitlab.example.com/data/sales-pipelines.git" onChange={(e) => setPl({ repo: e.target.value || undefined })} data-testid="pipelines-repo" />
                </label>
                <div className="row">
                  <label>Folder of the DAGs <input className="mono" value={pl.path ?? ""} placeholder="dags" onChange={(e) => setPl({ path: e.target.value || undefined })} /></label>
                  <label>Branch (or tag) <input className="mono" value={pl.ref ?? ""} placeholder="main" onChange={(e) => setPl({ ref: e.target.value || undefined })} /></label>
                  <label>Connection with its token or key
                    <input className="mono" value={pl.credentials?.secretRef ?? ""} placeholder={`${p.metadata.name}-git`} onChange={(e) => setPl({ credentials: e.target.value ? { secretRef: e.target.value } : undefined })} data-testid="pipelines-credentials" />
                  </label>
                </div>
                <p className="field-help">
                  Merge-request reviews (7.3), for a GitLab repo: each open merge request's dbt change is built on a data branch of its own, and the merge request told.
                </p>
                <div className="row">
                  <label>GitLab address (the server's) <input className="mono" value={pl.reviews?.gitlab ?? ""} placeholder={(() => { try { const u = new URL(pl.repo ?? ""); return `${u.protocol}//${u.host}`; } catch { return "https://gitlab.example.com"; } })()} onChange={(e) => setReviews({ gitlab: e.target.value })} /></label>
                  <label>Project path (empty: the repo's own) <input className="mono" value={pl.reviews?.project ?? ""} placeholder={(() => { try { return new URL(pl.repo ?? "").pathname.replace(/^\/|\.git$/g, "") || "group/repo"; } catch { return "group/repo"; } })()} onChange={(e) => setReviews({ project: e.target.value || undefined })} /></label>
                  <label>dbt folder <input className="mono" value={pl.reviews?.dbt ?? ""} placeholder="dags/dbt" onChange={(e) => setReviews({ dbt: e.target.value })} /></label>
                </div>
              </div>
            );
          })()}

          <h2>Alerts</h2>
          <p className="field-help">
            Where the project is told when a pipeline's run fails, a check fails, or a table isn't changed in time.
          </p>
          <label>Teams channel: the name of a connection of the project's, holding the channel's webhook
            <input value={spec.notify?.teams ?? ""} placeholder="alerts" onChange={(e) => setNotify({ teams: e.target.value || undefined })} />
          </label>
          <label>Slack channel: the name of a connection holding its incoming webhook
            <input value={spec.notify?.slack ?? ""} placeholder="alerts-slack" onChange={(e) => setNotify({ slack: e.target.value || undefined })} />
          </label>
          <label>Email
            <input value={(spec.notify?.email ?? []).join(", ")} placeholder="sales-data@example.com" onChange={(e) => setNotify({ email: list(e.target.value).length ? list(e.target.value) : undefined })} />
          </label>
          {spec.tables && (
            <>
              <div className="field-label" style={{ marginBottom: 6 }}>Tables that must change in time</div>
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

          {spec.tables && (
            <>
              <h2>Imports</h2>
              <p className="field-help">
                An application's data (any REST API), or a database's by a column that grows, copied into the project's tables on a
                schedule: each as &lt;import&gt;_&lt;name&gt;, merged on its primary key or appended to, each run copying only what's past its cursor.
                They're the project's pipelines. The connection holds the API's token (key token), or the database's account.
              </p>
              {(spec.imports ?? []).map((im, i) => (
                <div key={i} className="card stack" style={{ gap: 6, padding: 12 }} data-testid="import">
                  <div className="row">
                    <input value={im.name} placeholder="crm" style={{ width: 120 }} aria-label="Import name" onChange={(e) => setImport(i, { name: e.target.value })} />
                    <select value={im.source} aria-label="Import source" onChange={(e) => setImport(i, e.target.value === "rest_api"
                      ? { source: "rest_api", database: undefined, api: im.api ?? { baseUrl: "https://", resources: [{ name: "", path: "" }] } }
                      : { source: "sql_database", api: undefined, database: im.database ?? { kind: "postgresql", tables: [{ table: "" }] } })}>
                      <option value="rest_api">a REST API</option>
                      <option value="sql_database">a database, by a cursor</option>
                    </select>
                    <input value={im.connection ?? ""} placeholder="connection" aria-label="Import connection" onChange={(e) => setImport(i, { connection: e.target.value || undefined })} />
                    <input value={im.schedule ?? ""} placeholder="hourly (or cron: 0 * * * *)" aria-label="Import schedule" onChange={(e) => setImport(i, { schedule: e.target.value || undefined })} />
                    <button className="btn" onClick={() => set({ imports: spec.imports!.filter((_, j) => j !== i).length ? spec.imports!.filter((_, j) => j !== i) : undefined })}>Remove</button>
                    {name && im.name && (
                      <button className="btn" title="Its tables and where it got to dropped, then all copied again"
                        onClick={async () => { try { await resetImport(name, im.name); setResetDone(im.name); } catch (e) { setError((e as Error).message); } }}>
                        {resetDone === im.name ? "Copying all again…" : "Reset"}
                      </button>
                    )}
                  </div>
                  {im.source === "rest_api" && im.api && (
                    <>
                      <input value={im.api.baseUrl} placeholder="https://api.example.com/v1" aria-label="Base URL" onChange={(e) => setImport(i, { api: { ...im.api!, baseUrl: e.target.value } })} />
                      {im.api.resources.map((r, k) => (
                        <div key={k} className="row" style={{ flexWrap: "wrap" }}>
                          <input value={r.name} placeholder="leads" style={{ width: 110 }} aria-label="Resource name" onChange={(e) => setResource(i, k, { name: e.target.value })} />
                          <input value={r.path} placeholder="path: leads" aria-label="Path" onChange={(e) => setResource(i, k, { path: e.target.value })} />
                          <input value={r.dataSelector ?? ""} placeholder="where in the answer: data" aria-label="Data selector" onChange={(e) => setResource(i, k, { dataSelector: e.target.value || undefined })} />
                          <input value={r.primaryKey ?? ""} placeholder="primary key: id" style={{ width: 130 }} aria-label="Primary key" onChange={(e) => setResource(i, k, { primaryKey: e.target.value || undefined })} />
                          <input value={r.cursor ?? ""} placeholder="cursor: updated_at" style={{ width: 150 }} aria-label="Cursor" onChange={(e) => setResource(i, k, { cursor: e.target.value || undefined })} />
                          <input value={r.cursorParam ?? ""} placeholder="its parameter: since" style={{ width: 150 }} aria-label="Cursor parameter" onChange={(e) => setResource(i, k, { cursorParam: e.target.value || undefined })} />
                          <input value={r.cursorStart ?? ""} placeholder="from: 2026-01-01" style={{ width: 140 }} aria-label="Cursor start" onChange={(e) => setResource(i, k, { cursorStart: e.target.value || undefined })} />
                          <button className="btn" onClick={() => setImport(i, { api: { ...im.api!, resources: im.api!.resources.filter((_, j) => j !== k) } })}>Remove</button>
                        </div>
                      ))}
                      <button className="btn" onClick={() => setImport(i, { api: { ...im.api!, resources: [...im.api!.resources, { name: "", path: "" }] } })}>Add a resource</button>
                    </>
                  )}
                  {im.source === "sql_database" && im.database && (
                    <>
                      <select value={im.database.kind} aria-label="Database kind" onChange={(e) => setImport(i, { database: { ...im.database!, kind: e.target.value as "postgresql" | "mysql" } })}>
                        <option value="postgresql">postgresql</option>
                        <option value="mysql">mysql</option>
                      </select>
                      {im.database.tables.map((t, k) => (
                        <div key={k} className="row">
                          <input value={t.table} placeholder="schema.table" aria-label="Table" onChange={(e) => setDBTable(i, k, { table: e.target.value })} />
                          <input value={t.cursor ?? ""} placeholder="cursor: updated_at" aria-label="Cursor column" onChange={(e) => setDBTable(i, k, { cursor: e.target.value || undefined })} />
                          <input value={t.primaryKey ?? ""} placeholder="primary key: id" aria-label="Primary key column" onChange={(e) => setDBTable(i, k, { primaryKey: e.target.value || undefined })} />
                          <button className="btn" onClick={() => setImport(i, { database: { ...im.database!, tables: im.database!.tables.filter((_, j) => j !== k) } })}>Remove</button>
                        </div>
                      ))}
                      <button className="btn" onClick={() => setImport(i, { database: { ...im.database!, tables: [...im.database!.tables, { table: "" }] } })}>Add a table</button>
                    </>
                  )}
                </div>
              ))}
              <button className="btn" onClick={() => set({ imports: [...(spec.imports ?? []), { name: "", source: "rest_api", api: { baseUrl: "https://", resources: [{ name: "", path: "" }] } }] })}>Add an import</button>
            </>
          )}

          <h2>Sources</h2>
          <p className="field-help">
            The company's databases, queried where they are: each a read-only catalog the project's editors and pipelines may read
            (its readers too, where ticked). The connection is one of the project's, with the database's read-only account.
            Tables to copy come into the project's own as &lt;source&gt;_&lt;table&gt;, every change within minutes; their account then needs
            the database's change log (PostgreSQL: REPLICATION, and wal_level=logical; SQL Server: CDC on the tables; MySQL: the row binlog),
            which its administrator grants.
          </p>
          {(spec.sources ?? []).map((s, i) => (
            <div key={i} className="stack" style={{ gap: 6 }}>
            <div className="row" data-testid="source">
              <input value={s.name} placeholder="finance" style={{ width: 140 }} aria-label="Source name" onChange={(e) => setSource(i, { name: e.target.value })} />
              <select value={s.kind} aria-label="Kind" onChange={(e) => setSource(i, { kind: e.target.value as Source["kind"] })}>
                {(["postgresql", "mysql", "sqlserver", "oracle"] as const).map((k) => <option key={k} value={k}>{k}</option>)}
              </select>
              <input value={s.connection} placeholder="connection" aria-label="Connection" onChange={(e) => setSource(i, { connection: e.target.value })} />
              <input value={(s.schemas ?? []).join(", ")} placeholder="schemas: all" aria-label="Schemas" title="Only these schemas (default all)"
                onChange={(e) => setSource(i, { schemas: list(e.target.value).length ? list(e.target.value) : undefined })} />
              {s.kind !== "oracle" && (
                <input value={(s.copy?.tables ?? []).join(", ")} placeholder="copy: schema.table, …" aria-label="Tables to copy"
                  title="Copied into the project's tables by change capture, as <source>_<table>: every change within minutes (Kubernetes)"
                  onChange={(e) => setSource(i, { copy: list(e.target.value).length ? { tables: list(e.target.value) } : undefined })} />
              )}
              <label className="check">
                <input type="checkbox" checked={!!s.readers} onChange={(e) => setSource(i, e.target.checked ? { readers: true } : { readers: undefined, masks: undefined, rowFilters: undefined })} />Readers
              </label>
              {s.name && <span className="small muted mono">{sourceCatalog(p.metadata.name || "project", s)}</span>}
              <button className="btn" onClick={() => set({ sources: (spec.sources ?? []).filter((_, j) => j !== i).length ? spec.sources!.filter((_, j) => j !== i) : undefined })}>Remove</button>
            </div>
            {s.readers && (
              <div style={{ paddingLeft: 16 }}>
                <div className="small muted">What its readers see: tables as schema.table</div>
                {(s.rowFilters ?? []).map((f, k) => (
                  <div key={`f${k}`} className="row">
                    <input value={f.table} placeholder="schema.table" aria-label="Row filter table" onChange={(e) => setSource(i, { rowFilters: s.rowFilters!.map((x, j) => (j === k ? { ...x, table: e.target.value } : x)) })} />
                    <input className="grow" value={f.filter} placeholder="region = 'EU'" aria-label="Row filter" onChange={(e) => setSource(i, { rowFilters: s.rowFilters!.map((x, j) => (j === k ? { ...x, filter: e.target.value } : x)) })} />
                    <button className="btn" onClick={() => setSource(i, { rowFilters: s.rowFilters!.filter((_, j) => j !== k).length ? s.rowFilters!.filter((_, j) => j !== k) : undefined })}>Remove</button>
                  </div>
                ))}
                {(s.masks ?? []).map((m, k) => (
                  <div key={`m${k}`} className="row">
                    <input value={m.table} placeholder="schema.table" aria-label="Mask table" onChange={(e) => setSource(i, { masks: s.masks!.map((x, j) => (j === k ? { ...x, table: e.target.value } : x)) })} />
                    <input value={m.column} placeholder="column" aria-label="Mask column" onChange={(e) => setSource(i, { masks: s.masks!.map((x, j) => (j === k ? { ...x, column: e.target.value } : x)) })} />
                    <select value={m.type} aria-label="Mask type" onChange={(e) => setSource(i, { masks: s.masks!.map((x, j) => (j === k ? { ...x, type: e.target.value } : x)) })}>
                      {MASK_TYPES.map((t) => <option key={t}>{t}</option>)}
                    </select>
                    <button className="btn" onClick={() => setSource(i, { masks: s.masks!.filter((_, j) => j !== k).length ? s.masks!.filter((_, j) => j !== k) : undefined })}>Remove</button>
                  </div>
                ))}
                <div className="row">
                  <button className="btn" onClick={() => setSource(i, { rowFilters: [...(s.rowFilters ?? []), { table: "", filter: "" }] })}>Add a row filter</button>
                  <button className="btn" onClick={() => setSource(i, { masks: [...(s.masks ?? []), { table: "", column: "", type: "MASK" }] })}>Add a mask</button>
                </div>
              </div>
            )}
            </div>
          ))}
          <button className="btn" onClick={() => set({ sources: [...(spec.sources ?? []), { name: "", kind: "postgresql", connection: "" }] })}>Add a source</button>

          {spec.tables && (
            <>
              <h2>Table maintenance</h2>
              <p className="field-help">Each night, the tables' small files are compacted into larger ones.</p>
              <label className="check">
                <input type="checkbox" checked={!!spec.tables.maintenance?.paused} onChange={(e) => setMaintenance({ paused: e.target.checked || undefined })} />Paused
              </label>
            </>
          )}
        </div>
      )}

      {p.metadata.name && (
        <>
          <h2 style={{ margin: "28px 0 12px" }}>What each role will get</h2>
          <div className="grid cards">
            {grants(p.metadata.name, p.spec).map((g) => (
              <div key={g.role} className="card"><div className="card-head" style={{ marginBottom: 4 }}><h2>{g.role}</h2><span className="pill mono">{g.group}</span></div><ul>{g.items.map((i) => <li key={i}>{i}</li>)}</ul></div>
            ))}
          </div>
        </>
      )}
      <div className="page-foot">
        <button className="btn primary" onClick={save} disabled={saving} data-testid="save-project">{saving ? "Saving…" : "Save"}</button>
        <button className="btn" onClick={() => navigate(-1)}>Cancel</button>
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
