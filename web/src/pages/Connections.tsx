import { useState } from "react";
import { addConnection, changeConnection, getConnections, removeConnection, type Connection, type ConnectionKind } from "../api";
import { ErrorBanner, Spinner, usePoll, useProject } from "../lib";

// The credentials the project's editors give the platform: kept as the
// project's own, named in its spec (a repo's token, a database's user, a
// delivery's server). Once given, a secret value is never shown again; a
// new one is pasted over it.
export default function Connections() {
  const { name } = useProject();
  const { data, error, reload } = usePoll(() => getConnections(name), [name], 30_000);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<string>();
  const [actionError, setActionError] = useState<string>();
  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  const kindOf = (k?: string) => data.kinds.find((x) => x.kind === k);
  const remove = async (c: string) => {
    setActionError(undefined);
    try { await removeConnection(name, c); reload(); } catch (e) { setActionError((e as Error).message); }
  };
  return (
    <div className="page stack" data-testid="connections">
      <p className="lead" style={{ margin: 0 }}>
        Credentials the project's pipelines, sources, imports and deliveries use, given to the platform once and kept as the project's own.
        The spec names them. A password, token or key is never shown again: to rotate one, paste the new value.
      </p>
      <ErrorBanner error={actionError} />
      {data.canEdit && !adding && <div><button className="btn primary" onClick={() => setAdding(true)} data-testid="add-connection">Add a connection</button></div>}
      {adding && <ConnectionForm project={name} kinds={data.kinds} onDone={() => { setAdding(false); reload(); }} />}
      {data.connections.length === 0 && <p className="muted">No connections yet.</p>}
      {data.connections.map((c) => (
        <div key={c.name} className="card stack" style={{ gap: 8 }} data-testid={`connection-${c.name}`}>
          <div className="row">
            <h3 style={{ margin: 0 }} className="mono">{c.name}</h3>
            <span className="pill">{kindOf(c.kind)?.label ?? "made outside the platform"}</span>
            <span className="grow" />
            {data.canEdit && editing !== c.name && <button className="btn small" onClick={() => setEditing(c.name)}>New values</button>}
            {data.canEdit && (
              <button className="btn small" disabled={c.usedBy.length > 0} title={c.usedBy.length ? `Used by ${c.usedBy.join(", ")}` : undefined}
                onClick={() => { if (confirm(`Remove ${c.name}?`)) remove(c.name); }}>Remove</button>
            )}
          </div>
          <Values c={c} kind={kindOf(c.kind)} />
          <div className="small muted">
            {c.addedBy ? `Added by ${c.addedBy} ${c.addedAt ? new Date(c.addedAt).toLocaleString() : ""}.` : ""}
            {c.changedBy ? ` Changed by ${c.changedBy} ${c.changedAt ? new Date(c.changedAt).toLocaleString() : ""}.` : ""}
            {c.usedBy.length > 0 ? ` Used by ${c.usedBy.join(", ")}.` : " Not used by the project's spec."}
          </div>
          {editing === c.name && (
            <ConnectionForm project={name} kinds={data.kinds} existing={c} onDone={() => { setEditing(undefined); reload(); }} />
          )}
        </div>
      ))}
    </div>
  );
}

function Values({ c, kind }: { c: Connection; kind?: ConnectionKind }) {
  const label = (k: string) => kind?.fields.find((f) => f.key === k)?.label ?? k;
  const shown = Object.entries(c.values);
  return (
    <div className="small">
      {shown.map(([k, v]) => (
        <div key={k}>{label(k)}: <span className="mono" style={{ wordBreak: "break-all" }}>{v.length > 120 ? v.slice(0, 120) + "…" : v}</span></div>
      ))}
      {c.set.length > 0 && <div className="muted">Kept, not shown: {c.set.map(label).join(", ")}</div>}
    </div>
  );
}

function ConnectionForm({ project, kinds, existing, onDone }: { project: string; kinds: ConnectionKind[]; existing?: Connection; onDone: () => void }) {
  const [kind, setKind] = useState(existing?.kind || kinds[0].kind);
  const [cname, setName] = useState(existing?.name ?? `${project}-`);
  const [values, setValues] = useState<Record<string, string>>(existing?.values ?? {});
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string>();
  const k = kinds.find((x) => x.kind === kind)!;
  const save = async () => {
    setBusy(true);
    setErr(undefined);
    try {
      if (existing) await changeConnection(project, existing.name, existing.kind ? undefined : kind, values);
      else await addConnection(project, { name: cname, kind, values });
      onDone();
    } catch (e) { setErr((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <form className="card stack" style={{ gap: 8 }} onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="connection-form">
      <div className="row">
        {!existing && <label>Name <input className="mono" value={cname} onChange={(e) => setName(e.target.value)} data-testid="connection-name" /></label>}
        {!existing?.kind && (
          <label>Kind <select value={kind} onChange={(e) => { setKind(e.target.value); setValues({}); }} data-testid="connection-kind">
            {kinds.map((x) => <option key={x.kind} value={x.kind}>{x.label}</option>)}
          </select></label>
        )}
        <span className="small muted">For {k.for}.</span>
      </div>
      {k.fields.map((f) => (
        <label key={f.key} className="stack" style={{ gap: 2 }}>
          <span className="small">{f.label}{f.required ? " *" : ""}{f.secret && existing ? " (empty: as it is)" : ""}</span>
          {f.multiline
            ? <textarea className="mono" rows={4} value={values[f.key] ?? ""} onChange={(e) => setValues({ ...values, [f.key]: e.target.value })} spellCheck={false} />
            : <input className="mono" type={f.secret ? "password" : "text"} autoComplete="off" value={values[f.key] ?? ""}
                onChange={(e) => setValues({ ...values, [f.key]: e.target.value })} data-testid={`field-${f.key}`} />}
        </label>
      ))}
      {k.oneOf && <div className="small muted">One of {k.oneOf.join(", ")} at least.</div>}
      <ErrorBanner error={err} />
      <div className="row">
        <button className="btn primary" disabled={busy} data-testid="save-connection">{busy ? "Saving…" : existing ? "Save new values" : "Add"}</button>
        <button type="button" className="btn" onClick={onDone}>Cancel</button>
      </div>
    </form>
  );
}
