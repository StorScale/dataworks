import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { getConnections, getConnectors, putImport, removeImport, resetImport, type Connection, type Connector, type Import, type SAPExtraction } from "../api";
import { Empty, Intro } from "../components";
import { can, ErrorBanner, Spinner, useProject } from "../lib";

const sourceLabel: Record<Import["source"], string> = { rest_api: "a REST API", sql_database: "a database", salesforce: "Salesforce", gong: "Gong", highspot: "HighSpot", sap: "SAP S/4HANA", files: "uploaded files" };

// The project's imports: applications' and databases' data copied into its
// tables on a schedule (dlt). Its editors add one from a connector's form
// (use case 8.4): sign in (a connection), pick what to copy, and when.
export default function Imports() {
  const { name, view, reload } = useProject();
  const [adding, setAdding] = useState<Connector>();
  const [connectors, setConnectors] = useState<Connector[]>();
  const [error, setError] = useState<string>();
  useEffect(() => { getConnectors().then((r) => setConnectors(r.connectors)).catch((e) => setError(e.message)); }, []);
  if (!view?.spec) return <div className="page"><Spinner /></div>;
  const imports = view.spec.spec.imports ?? [];
  const canEdit = can(view, "connections");
  const act = async (f: () => Promise<unknown>) => { setError(undefined); try { await f(); reload(); } catch (e) { setError((e as Error).message); } };
  return (
    <div className="page stack" data-testid="imports">
      <Intro actions={canEdit && !adding && connectors && connectors.map((c) => <button key={c.source} className="btn primary" onClick={() => setAdding(c)} data-testid={`add-${c.source}`}>Add {c.label}</button>)}>
        Data copied into the project's tables on a schedule, each object into its own table, only what changed after the first run.
        A connector signs in with one of the project's <Link to={`/p/${name}/connections`}>connections</Link>.
      </Intro>
      <ErrorBanner error={error} />
      {adding && <ConnectorForm project={name} connector={adding} taken={imports.map((i) => i.name)} onDone={() => { setAdding(undefined); reload(); }} />}
      {imports.length === 0 && !adding && <Empty>No imports yet.</Empty>}
      {imports.map((im) => { const app = im.salesforce ?? im.gong ?? im.highspot ?? im.sap; return (
        <div key={im.name} className="card stack" style={{ gap: 6 }} data-testid={`import-${im.name}`}>
          <div className="card-head">
            <h2 className="mono">{im.name}<span className="pill">{sourceLabel[im.source]}</span></h2>
            <div className="row">
            {canEdit && <button className="btn small" onClick={() => act(() => resetImport(name, im.name))}>Copy all again</button>}
            {canEdit && <button className="btn small danger" onClick={() => { if (confirm(`Remove ${im.name}? Its tables stay.`)) act(() => removeImport(name, im.name)); }}>Remove</button>}
            </div>
          </div>
          <div className="small">
            {app && app.objects.length > 0 && <>Objects: <span className="mono">{app.objects.join(", ")}</span>{app.since ? `, from ${app.since}` : ""}. </>}
            {(im.sap?.odp ?? []).length > 0 && <>ODP, by SAP's delta: <span className="mono">{im.sap!.odp!.map((x) => `${x.service}/${x.entitySet} → ${im.name}_${x.name}`).join(", ")}</span>. </>}
            {im.api && <>From <span className="mono">{im.api.baseUrl}</span>: {im.api.resources.map((r) => r.name).join(", ")}. </>}
            {im.database && <>Tables: <span className="mono">{im.database.tables.map((t) => t.table).join(", ")}</span>. </>}
            {im.files && <>Files: <span className="mono">{im.files.tables.map((t) => `${t.file} → ${im.name}_${t.name}`).join(", ")}</span> (from the landing area). </>}
            Into <span className="mono">{im.name}_*</span>, {im.schedule ? <>at <span className="mono">{im.schedule}</span> (UTC)</> : "hourly"}
            {im.connection && <>, signed in with <span className="mono">{im.connection}</span></>}.
          </div>
        </div>
      ); })}
    </div>
  );
}

// What a connector's form ticks to start with: a few of a large application's objects; all of a small one's.
const STARTING: Partial<Record<Connector["source"], string[]>> = {
  salesforce: ["account", "opportunity"],
  sap: ["business_partners", "sales_orders", "sales_order_items"],
};

function ConnectorForm({ project, connector, taken, onDone }: { project: string; connector: Connector; taken: string[]; onDone: () => void }) {
  const [conns, setConns] = useState<Connection[]>();
  const [kindLabel, setKindLabel] = useState(connector.connection);
  const [imName, setName] = useState(({ salesforce: "crm", sap: "erp" } as Record<string, string>)[connector.source] ?? connector.source);
  const [connection, setConnection] = useState("");
  const [objects, setObjects] = useState<string[]>(STARTING[connector.source] ?? connector.objects);
  const [schedule, setSchedule] = useState("0 * * * *");
  const [since, setSince] = useState("");
  const [odp, setOdp] = useState<SAPExtraction[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  useEffect(() => {
    getConnections(project).then((r) => {
      const mine = r.connections.filter((c) => c.kind === connector.connection);
      setConns(mine);
      setKindLabel(r.kinds.find((k) => k.kind === connector.connection)?.label ?? connector.connection);
      if (mine[0]) setConnection(mine[0].name);
    }).catch((e) => setError(e.message));
  }, [project, connector.connection]);
  const save = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await putImport(project, { name: imName, source: connector.source, connection, schedule: schedule || undefined,
        [connector.source]: { objects, since: since || undefined, ...(odp.length ? { odp } : {}) } });
      onDone();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  if (!conns) return <Spinner />;
  return (
    <form className="card stack" onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="connector-form">
      <h2>Add {connector.label}</h2>
      <div className="fields">
        <label>Name<input className="mono" value={imName} onChange={(e) => setName(e.target.value)} />
          {taken.includes(imName) && <span className="field-help">Saving replaces the import of that name.</span>}</label>
        {conns.length > 0 && <label>Sign in with<select value={connection} onChange={(e) => setConnection(e.target.value)}>{conns.map((c) => <option key={c.name}>{c.name}</option>)}</select></label>}
      </div>
      {conns.length === 0 && (
        <div className="banner warn">No {connector.label} connection yet: add one on the <Link to={`/p/${project}/connections`}>Connections tab</Link> (kind “{kindLabel}”), then come back.</div>
      )}
      <div className="stack" style={{ gap: 8 }}>
        <span className="field-label">What to copy (each into {imName}_<i>object</i>; those marked ↻ copy only what changed)</span>
        <div className="checks">
          {connector.objects.map((o) => (
            <label key={o} className="check"><input type="checkbox" checked={objects.includes(o)}
              onChange={(e) => setObjects(e.target.checked ? [...objects, o] : objects.filter((x) => x !== o))} /><span className="mono">{o}</span>{connector.incremental.includes(o) ? " ↻" : ""}</label>
          ))}
        </div>
      </div>
      {connector.source === "sap" && <ODPRows rows={odp} prefix={imName} onChange={setOdp} />}
      <div className="fields">
        <label>When (cron, UTC)<input className="mono" value={schedule} onChange={(e) => setSchedule(e.target.value)} /></label>
        {connector.incremental.length > 0 && <label>History from<input type="date" value={since} onChange={(e) => setSince(e.target.value)} /><span className="field-help">Empty: all of it.</span></label>}
      </div>
      <ErrorBanner error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !connection || objects.length === 0 && odp.length === 0} data-testid="save-import">{busy ? "Saving…" : "Add"}</button>
        <button type="button" className="btn" onClick={onDone}>Cancel</button>
      </div>
    </form>
  );
}

// The company's own ODP extractions an SAP import copies: each a CDS view or an extractor its gateway serves as OData
// (made in SEGW: Redefine, ODP Extraction), copied whole once and then by SAP's delta, deletions included.
function ODPRows({ rows, prefix, onChange }: { rows: SAPExtraction[]; prefix: string; onChange: (rows: SAPExtraction[]) => void }) {
  const set = (i: number, patch: Partial<SAPExtraction>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className="stack" style={{ gap: 8 }} data-testid="odp-rows">
      <span className="field-label">ODP extractions (CDS views or extractors your gateway serves; copied by SAP's own delta, deletions included)</span>
      {rows.map((r, i) => (
        <div key={i} className="fields">
          <label>Name<input className="mono" value={r.name} placeholder="sales_items" onChange={(e) => set(i, { name: e.target.value })} />
            <span className="field-help">Its table: {prefix}_{r.name || "name"}</span></label>
          <label>Service<input className="mono" value={r.service} placeholder="ZSALES_ODP_SRV" onChange={(e) => set(i, { service: e.target.value })} /></label>
          <label>Entity set<input className="mono" value={r.entitySet} placeholder="EntityOfZCDS_SALES_ITEM" onChange={(e) => set(i, { entitySet: e.target.value })} /></label>
          <label>Key fields<input className="mono" value={r.primaryKey.join(", ")} placeholder="SalesOrder, SalesOrderItem"
            onChange={(e) => set(i, { primaryKey: e.target.value.split(",").map((f) => f.trim()).filter(Boolean) })} /></label>
          <button type="button" className="btn small" onClick={() => onChange(rows.filter((_, j) => j !== i))}>Remove</button>
        </div>
      ))}
      <div><button type="button" className="btn small" onClick={() => onChange([...rows, { name: "", service: "", entitySet: "", primaryKey: [] }])}
        data-testid="add-odp">Add an ODP extraction</button></div>
    </div>
  );
}
