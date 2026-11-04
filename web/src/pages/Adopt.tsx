import { useEffect, useState } from "react";
import { type AdoptableTable, type Adoption, adoptTables, ago, getAdoptableSchemas, getAdoptableStores, getAdoptableTables, giveBack, stopAdopting, takeOver } from "../api";
import { ErrorBanner, Spinner } from "../lib";

// Adopting another platform's Iceberg tables where they are (tables.adopt),
// and taking one over once that platform stops writing it: for the
// project's editors, who may write its tables. The users' guide, Bringing
// data in: Adopted tables.

const size = (b: number) => (b >= 1e9 ? `${(b / 1e9).toFixed(1)} GB` : b >= 1e6 ? `${(b / 1e6).toFixed(1)} MB` : `${Math.max(1, Math.round(b / 1e3))} KB`);
const tableName = (n: string) => n.toLowerCase().replace(/[^a-z0-9_]/g, "_").replace(/^([0-9])/, "_$1").slice(0, 63);

// AdoptPanel picks a store, one of its schemas, and tables of it to adopt.
export function AdoptPanel({ project, adopted, onClose, onAdopted }: { project: string; adopted: Adoption[]; onClose: () => void; onAdopted: () => void }) {
  const [stores, setStores] = useState<{ name: string; takeover: boolean }[]>();
  const [store, setStore] = useState("");
  const [schemas, setSchemas] = useState<string[]>();
  const [schema, setSchema] = useState("");
  const [tables, setTables] = useState<AdoptableTable[]>();
  const [picked, setPicked] = useState<Record<string, string>>({}); // from -> name here
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const fail = (e: unknown) => setError((e as Error).message);

  useEffect(() => {
    getAdoptableStores(project).then((r) => { setStores(r.stores); if (r.stores.length === 1) setStore(r.stores[0].name); }).catch(fail);
  }, [project]);
  useEffect(() => {
    setSchemas(undefined); setSchema(""); setTables(undefined);
    if (store) getAdoptableSchemas(project, store).then((r) => setSchemas(r.schemas)).catch(fail);
  }, [project, store]);
  useEffect(() => {
    setTables(undefined); setPicked({});
    if (store && schema) getAdoptableTables(project, store, schema).then((r) => setTables(r.tables)).catch(fail);
  }, [project, store, schema]);

  const taken = new Set(adopted.map((a) => a.table));
  const names = Object.values(picked);
  const clash = names.find((n, i) => taken.has(n) || names.indexOf(n) !== i || !/^[a-z_][a-z0-9_]{0,62}$/.test(n));
  const adopt = async () => {
    setBusy(true); setError(undefined);
    try {
      await adoptTables(project, Object.entries(picked).map(([from, table]) => ({ table, from })));
      onAdopted();
    } catch (e) { fail(e); } finally { setBusy(false); }
  };

  return (
    <div className="card stack" data-testid="adopt-panel">
      <div className="card-head">
        <h2 style={{ fontSize: 18 }}>Adopt tables</h2>
        <button className="btn small" onClick={onClose}>Close</button>
      </div>
      <p className="muted small" style={{ margin: 0 }}>
        Another platform's Iceberg tables, taken into this project where their files are: nothing is copied. They're read-only here
        while that platform writes them, followed as it does; once it stops, one can be taken over and become this project's own.
      </p>
      {error && <ErrorBanner error={error} />}
      {!stores ? <Spinner /> : stores.length === 0 ? <p className="muted">The install has no external stores to adopt from (the chart's <code>externalStores</code>).</p> : (
        <div className="row" style={{ gap: 12, flexWrap: "wrap" }}>
          <label className="field" style={{ minWidth: 180 }}>Store
            <select value={store} onChange={(e) => setStore(e.target.value)} data-testid="adopt-store">
              <option value="">Choose…</option>
              {stores.map((s) => <option key={s.name} value={s.name}>{s.name}</option>)}
            </select>
          </label>
          {store && (
            <label className="field" style={{ minWidth: 220 }}>Schema
              {!schemas ? <Spinner /> : (
                <select value={schema} onChange={(e) => setSchema(e.target.value)} data-testid="adopt-schema">
                  <option value="">Choose…</option>
                  {schemas.map((s) => <option key={s} value={s}>{s}</option>)}
                </select>
              )}
            </label>
          )}
        </div>
      )}
      {schema && !tables && <div className="row" style={{ gap: 8 }}><Spinner /> <span className="muted small">Reading its tables…</span></div>}
      {tables && (tables.length === 0 ? <p className="muted">No tables in {schema}.</p> : (
        <div className="scroll-x">
          <table className="t" data-testid="adopt-tables">
            <thead><tr><th></th><th>Table</th><th>Rows</th><th>Size</th><th>Last written</th><th>Its name here</th></tr></thead>
            <tbody>
              {tables.map((t) => {
                const on = t.from in picked;
                const can = !t.adoptedAs && !t.note;
                return (
                  <tr key={t.from} className={can ? "" : "muted"}>
                    <td><input type="checkbox" disabled={!can} checked={on} aria-label={`Adopt ${t.name}`} data-testid={`adopt-pick-${t.name}`}
                      onChange={(e) => setPicked((p) => { const n = { ...p }; if (e.target.checked) n[t.from] = tableName(t.name); else delete n[t.from]; return n; })} /></td>
                    <td className="mono">{t.name}</td>
                    <td style={{ whiteSpace: "nowrap" }}>{t.note ? "" : t.rows.toLocaleString()}</td>
                    <td style={{ whiteSpace: "nowrap" }}>{t.note ? "" : size(t.bytes)}</td>
                    <td className="small" style={{ whiteSpace: "nowrap" }}>{t.lastChange ? <span title={new Date(t.lastChange).toLocaleString()}>{ago(new Date(t.lastChange))}</span> : ""}</td>
                    <td>
                      {t.adoptedAs ? <span className="small">adopted as <code>{t.adoptedAs}</code></span>
                        : t.note ? <span className="small">{t.note}</span>
                        : on ? <input className="mono" style={{ width: 220 }} value={picked[t.from]} aria-label={`Name for ${t.name}`}
                            onChange={(e) => setPicked((p) => ({ ...p, [t.from]: e.target.value }))} /> : null}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ))}
      {names.length > 0 && (
        <div className="row" style={{ gap: 12 }}>
          <button className="btn primary" disabled={busy || !!clash} onClick={adopt} data-testid="adopt-go">
            Adopt {names.length} table{names.length > 1 ? "s" : ""}
          </button>
          {clash && <span className="small" style={{ color: "var(--red, #b00)" }}>{taken.has(clash) ? `The project already has a table ${clash}.` : `${clash || "A name"}: lowercase letters, digits and underscores, each name once.`}</span>}
          <span className="muted small">The operator registers them within a minute; the catalog lists them a little after.</span>
        </div>
      )}
    </div>
  );
}

// Adopted is an adopted table's banner: where it comes from, and taking it over (or no longer adopting it).
export function Adopted({ project, adoption, canWrite, onChanged }: { project: string; adoption: Adoption; canWrite: boolean; onChanged: () => void }) {
  const [confirm, setConfirm] = useState(false);
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const run = async (f: () => Promise<unknown>) => {
    setBusy(true); setError(undefined);
    try { await f(); setConfirm(false); onChanged(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  if (adoption.takenOver) {
    return (
      <div className="banner info stack" style={{ marginBottom: 12, gap: 8 }} data-testid="adopted">
        <div>
          <strong>Taken over</strong> from <code>{adoption.from}</code>{adoption.takenOverBy && <> by {adoption.takenOverBy}</>} on {new Date(adoption.takenOver).toLocaleDateString()}:
          it's this project's own now, where its files are.
        </div>
        {error && <ErrorBanner error={error} />}
        {canWrite && !confirm && (
          <div><button className="btn small" onClick={() => setConfirm(true)} data-testid="giveback">Give back…</button></div>
        )}
        {canWrite && confirm && (
          <div className="stack" style={{ gap: 8 }} data-testid="giveback-confirm">
            <div className="small">
              Giving it back puts it in its store's catalog again as it is now, everything written here included, for the platform it came
              from to write. This project adopts it again: read-only here, and followed. Its pipelines here must have stopped writing it.
            </div>
            <div className="row" style={{ gap: 12 }}>
              <button className="btn small primary" disabled={busy} onClick={() => run(() => giveBack(project, adoption.table))} data-testid="giveback-go">Give it back</button>
              <button className="btn small" disabled={busy} onClick={() => setConfirm(false)}>Cancel</button>
            </div>
          </div>
        )}
      </div>
    );
  }
  return (
    <div className="banner info stack" style={{ marginBottom: 12, gap: 8 }} data-testid="adopted">
      <div>
        <strong>Adopted</strong> from <code>{adoption.from}</code>: read-only here while that platform writes it, and followed as it does.
      </div>
      {error && <ErrorBanner error={error} />}
      {canWrite && !confirm && (
        <div className="row" style={{ gap: 12 }}>
          <button className="btn small" onClick={() => setConfirm(true)} data-testid="takeover">Take over…</button>
          <button className="linkish small" disabled={busy} onClick={() => run(() => stopAdopting(project, adoption.table))} data-testid="unadopt">Stop adopting</button>
        </div>
      )}
      {canWrite && confirm && (
        <div className="stack" style={{ gap: 8 }} data-testid="takeover-confirm">
          <div className="small">
            Taking it over makes it this project's own, where its files are: written, maintained and erased here, and no longer followed.
            The other platform must have stopped writing it. Where the platform may write that store's catalog, the table is taken out of
            it too, so its writers there no longer find it. It can be given back later.
          </div>
          <div className="row" style={{ gap: 12 }}>
            <button className="btn small primary" disabled={busy} onClick={() => run(() => takeOver(project, adoption.table))} data-testid="takeover-go">Take it over</button>
            <button className="btn small" disabled={busy} onClick={() => setConfirm(false)}>Cancel</button>
          </div>
        </div>
      )}
    </div>
  );
}
