import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { type ColumnProfile, SQLAnswer, TableStats, ago, classify, getClassification, getFlow, getHistory, getMaintenance, runMaintenance, runSQL, when } from "../api";
import { can, checkName, CheckPill, ErrorBanner, failing, limits, maskLabel, Spinner, usePoll, useProject } from "../lib";
import { FlowStrip } from "./Overview";

const TABS = ["columns", "preview", "lineage", "quality", "history", "storage"] as const;
type Tab = (typeof TABS)[number];

// Data is a project's tables: their columns, a look at their rows (as you,
// so your filters and masks apply), where they come from, their checks, and
// how they're stored (their files, as maintenance keeps them).
// holds is a column's profile in a few words: how much is null, how many values, their range.
function holds(p?: ColumnProfile, type?: string) {
  if (!p) return <span className="muted">–</span>;
  const parts: string[] = [];
  if (p.nulls !== undefined) parts.push(p.nulls === 0 ? "no nulls" : `${Math.round(p.nulls * 100)}% null`);
  if (p.distinct !== undefined) parts.push(`${p.distinct} distinct`);
  const text = /char|varchar|string|json/i.test(type ?? "");   // a text column's min and max are its values' lengths
  if (p.min !== undefined && p.max !== undefined) parts.push(text ? `${p.min} to ${p.max} characters` : `${p.min} to ${p.max}`);
  return parts.join(" · ") || <span className="muted">–</span>;
}

export default function Data() {
  const { name, view } = useProject();
  const spec = view?.spec?.spec;
  const [params, setParams] = useSearchParams();
  const flow = usePoll(() => getFlow(name), [name], 30_000);
  const klass = usePoll(() => getClassification(name), [name], 30_000);
  const [klassError, setKlassError] = useState<string>();
  const tables = flow.data?.tables.filter((t) => !t.external) ?? [];
  const table = tables.find((t) => t.name === params.get("table")) ?? tables[0];
  const tab: Tab = (TABS as readonly string[]).includes(params.get("tab") ?? "") ? (params.get("tab") as Tab) : "columns";
  const go = (t: string, tb: Tab = "columns") => setParams(tb === "columns" ? { table: t } : { table: t, tab: tb });

  if (flow.error && !flow.data) return <div className="page"><ErrorBanner error={flow.error} /></div>;
  if (!flow.data) return <div className="page"><Spinner /></div>;
  if (tables.length === 0) return <div className="page"><div className="empty">The catalog hasn't seen this project's tables yet. It looks every minute.</div></div>;

  const reader = !!view?.role && !can(view, "read-all"); // under the readers' rules
  const lim = limits(spec, table.name);
  const protectedOf = (col: string) => (klass.data?.columns ?? []).filter((c) => c.table === table.name && c.column === col);
  const decide = async (col: string, tag: string, action: "confirm" | "remove") => {
    try { await classify(name, table.name, col, tag, action); klass.reload(); } catch (e) { setKlassError((e as Error).message); }
  };
  const narrowed = lim.filters.length > 0 || Object.keys(lim.masks).length > 0 || lim.hidden;
  const into = flow.data.edges.filter((e) => e.to === table.fqn);
  const outOf = flow.data.edges.filter((e) => e.from === table.fqn);
  const short = (fqn: string) => flow.data!.tables.find((t) => t.fqn === fqn)?.name ?? fqn;
  const madeFrom = (col: string) => into.flatMap((e) => e.columns.filter((c) => c.to === col).flatMap((c) => c.from.map((f) => `${short(e.from)}.${f}`)));

  return (
    <div className="page">
      <div className="split">
        <nav className="side side-list" aria-label="Tables" data-testid="tables">
          <p className="eyebrow">Tables</p>
          {tables.map((t) => (
            <button key={t.fqn} className={t === table ? "active" : ""} onClick={() => go(t.name)} data-testid={`table-${t.name}`}>
              <span className="mono">{t.name}</span>
              {failing(t.checks).length > 0 ? <span className="dot red" title="a check is failing" /> : limits(spec, t.name).hidden ? <span className="small muted" style={{ whiteSpace: "nowrap" }} title="The project's readers can't read this table">editors</span> : null}
            </button>
          ))}
        </nav>

        <section className="main stack">
          <div className="card flush">
            <div style={{ padding: "18px 20px 0" }}>
              <div className="card-head" style={{ marginBottom: 4 }}>
                <h2 className="mono" style={{ fontSize: 18 }} data-testid="table-title">{table.name}</h2>
                <Link className="btn small" to={`../sql?table=${table.name}`} relative="path">Query in SQL</Link>
              </div>
              <p className="muted small" style={{ margin: "0 0 10px" }}>
                {table.description || `${table.fqn.split(".").slice(1).join(".")}`} · {table.columns.length} columns
                {into[0]?.pipeline && <> · made by <Link to={`../pipelines?pipeline=${into[0].pipeline}`} relative="path" className="mono">{into[0].pipeline}</Link></>}
              </p>
              {narrowed && (
                <div className={`banner ${reader ? "warn" : "info"}`} style={{ marginBottom: 12 }} data-testid="limits">
                  <div>
                    <strong>{reader ? "You see this table as a reader." : "Readers see less of this table."}</strong>{" "}
                    {lim.hidden && (reader ? "Only editors can read it. " : "Only editors can read it. ")}
                    {lim.filters.map((f) => <span key={f}>{reader ? "You see" : "They see"} only rows where <code>{f}</code>. </span>)}
                    {Object.entries(lim.masks).map(([c, t]) => <span key={c}><code>{c}</code> shows {maskLabel(t)}. </span>)}
                  </div>
                </div>
              )}
            </div>
            <div className="subtabs" role="tablist" style={{ padding: "0 8px" }}>
              {TABS.map((t) => (
                <button key={t} role="tab" aria-selected={t === tab} className={t === tab ? "active" : ""} onClick={() => go(table.name, t)} data-testid={`data-tab-${t}`}>
                  {t[0].toUpperCase() + t.slice(1)}
                  {t === "quality" && failing(table.checks).length > 0 && <span className="pill red" style={{ marginLeft: 6 }}>{failing(table.checks).length}</span>}
                </button>
              ))}
            </div>

            {tab === "columns" && klassError && <ErrorBanner error={klassError} />}
            {tab === "columns" && (
              <div className="scroll-x">
                {table.rows !== undefined && <p className="small muted" data-testid="profile">{table.rows} rows{table.profiledAt ? `, profiled ${ago(new Date(table.profiledAt))}` : ""}.</p>}
                <table className="t" data-testid="columns">
                  <thead><tr><th>Column</th><th>Type</th><th>Holds</th><th>Readers see</th><th>Protection</th><th>Comes from</th></tr></thead>
                  <tbody>
                    {table.columns.map((c) => (
                      <tr key={c.name}>
                        <td className="mono">{c.name}</td>
                        <td className="mono muted">{c.type || "–"}</td>
                        <td className="small" data-testid={`profile-${c.name}`}>{holds(c.profile, c.type)}</td>
                        <td>{lim.hidden ? <span className="pill amber">nothing</span> : lim.masks[c.name] ? <span className="pill amber">{maskLabel(lim.masks[c.name])}</span> : <span className="muted small">all of it</span>}</td>
                        <td data-testid={`protection-${c.name}`}>
                          {protectedOf(c.name).map((pc) => (
                            <span key={pc.tag} className="row" style={{ gap: 6, display: "inline-flex" }}>
                              <span className={"pill " + (pc.state === "confirmed" ? "amber" : "blue")} title={pc.declared ? "declared by the project" : pc.automated ? "found by the catalog" : "tagged in the catalog"}>
                                {pc.tag}{pc.state === "suggested" ? " (suggested)" : ""}
                              </span>
                              {!reader && pc.state === "suggested" && <button className="linkish small" onClick={() => decide(c.name, pc.tag, "confirm")} data-testid={`confirm-${c.name}`}>confirm</button>}
                              {!reader && !pc.declared && <button className="linkish small" onClick={() => decide(c.name, pc.tag, "remove")} data-testid={`unprotect-${c.name}`}>remove</button>}
                            </span>
                          ))}
                          {protectedOf(c.name).length === 0 && <span className="muted small">–</span>}
                        </td>
                        <td className="mono small muted">{madeFrom(c.name).join(", ") || ""}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            {tab === "preview" && <Preview project={name} table={table.name} key={table.name} />}
            {tab === "lineage" && (
              <div style={{ padding: 20 }} className="stack">
                <FlowStrip flow={flow.data} project={name} here={table.name} />
                {[...into, ...outOf].length === 0 ? <p className="muted">No pipeline has reported reading or writing this table yet.</p> : (
                  <table className="t" data-testid="edges">
                    <thead><tr><th>From</th><th>To</th><th>Pipeline</th><th>Columns</th></tr></thead>
                    <tbody>
                      {[...into, ...outOf].map((e, i) => (
                        <tr key={i}>
                          <td className="mono">{short(e.from)}</td>
                          <td className="mono">{short(e.to)}</td>
                          <td className="mono">{e.pipeline ?? <span className="muted">unknown</span>}</td>
                          <td className="small">{e.columns.map((c) => <div key={c.to}><code>{c.to}</code> ← {c.from.map((f) => <code key={f}>{f} </code>)}</div>)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            )}
            {tab === "quality" && (
              table.checks.length === 0 ? <p className="muted" style={{ padding: 20 }}>No checks on this table.</p> : (
                <table className="t" data-testid="checks">
                  <thead><tr><th>Check</th><th>Status</th><th>Last result</th></tr></thead>
                  <tbody>
                    {table.checks.map((c) => (
                      <tr key={c.name}>
                        <td className="mono">{checkName(name, table.name, c.name)}</td>
                        <td><CheckPill status={c.status} /></td>
                        <td className="small">{c.result || <span className="muted">–</span>}{c.when ? <div className="muted">{new Date(c.when).toLocaleString()}</div> : null}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )
            )}
            {tab === "history" && <History project={name} table={table.name} key={table.name} />}
            {tab === "storage" && <Storage project={name} table={table.name} canRun={can(view, "run-pipelines")} />}
          </div>
        </section>
      </div>
    </div>
  );
}

const size = (b: number) => b >= 1 << 30 ? `${(b / (1 << 30)).toFixed(1)} GB` : b >= 1 << 20 ? `${(b / (1 << 20)).toFixed(1)} MB` : `${Math.round(b / 1024)} KB`;
// duration is an ISO 8601 duration (P7D, PT12H) in words.
function duration(iso: string): string {
  const m = iso.match(/^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?)?$/);
  if (!m) return iso;
  const [d, h, mi] = [m[1], m[2], m[3]].map((x) => Number(x ?? 0));
  const part = (n: number, unit: string) => (n ? `${n} ${unit}${n === 1 ? "" : "s"}` : "");
  return [part(d, "day"), part(h, "hour"), part(mi, "minute")].filter(Boolean).join(" ") || iso;
}
const counts = (s: TableStats) => `${s.dataFiles} files, ${size(s.bytes)}`;

// History is a table's changes, newest first: when, what each did, who made
// it, the project's tags on it; each can be read as it was then (as you, with
// your filters and masks).
function History({ project, table }: { project: string; table: string }) {
  const h = usePoll(() => getHistory(project, table), [project, table], 30_000);
  const navigate = useNavigate();
  if (h.error && !h.data) return <div style={{ padding: 20 }}><ErrorBanner error={h.error} /></div>;
  if (!h.data) return <div style={{ padding: 20 }}><Spinner /></div>;
  const read = (q: string) => navigate(`/p/${project}/sql?sql=${encodeURIComponent(`SELECT *\nFROM ${q}\nLIMIT 100`)}`);
  return (
    <div style={{ padding: 20 }} className="stack" data-testid="history">
      <p className="small muted" style={{ margin: 0 }}>
        Each change to the table, newest first.{h.data.keepHistory && h.data.keepHistory !== "0s" ? ` The data as it was is kept ${h.data.keepHistory.replace(/h0m0s$/, "h")} back, and as long as a tag holds it.` : ""}
      </p>
      {h.data.changes.length === 0 ? <p className="muted">No changes recorded in the history kept.</p> : (
        <table className="t" data-testid="changes">
          <thead><tr><th>When</th><th>What</th><th>Who</th><th></th></tr></thead>
          <tbody>
            {h.data.changes.map((c) => (
              <tr key={c.hash}>
                <td className="small" style={{ whiteSpace: "nowrap" }}>{new Date(c.time).toLocaleString()}<div className="muted mono">{c.hash.slice(0, 12)}</div></td>
                <td className="small">
                  {c.stats ? <>{c.stats.operation ?? "change"}: {c.stats.addedRows ? `+${c.stats.addedRows} rows` : ""} {c.stats.deletedRows ? `−${c.stats.deletedRows} rows` : ""} ({c.stats.rows} then)</> : <span className="muted">{c.message}</span>}
                  {(c.tags ?? []).map((t) => <span key={t} className="pill blue" style={{ marginLeft: 6 }}>{t}</span>)}
                </td>
                <td className="small">{c.who || <span className="muted">–</span>}</td>
                <td>{c.kept ? <button className="linkish small" onClick={() => read(c.query)}>Read it as it was</button> : <span className="muted small">past the history kept</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// Storage is a table's files now, and what maintenance last did.
function Storage({ project, table, canRun }: { project: string; table: string; canRun: boolean }) {
  const m = usePoll(() => getMaintenance(project), [project], 15_000);
  const [error, setError] = useState<string>();
  if (m.error && !m.data) return <div style={{ padding: 20 }}><ErrorBanner error={m.error} /></div>;
  if (!m.data) return <div style={{ padding: 20 }}><Spinner /></div>;
  const d = m.data;
  const t = d.tables.find((x) => x.name === table);
  const now = t?.now;
  const run = t?.lastRun;
  const ask = async () => {
    setError(undefined);
    try { await runMaintenance(project); m.reload(); } catch (e) { setError((e as Error).message); }
  };
  return (
    <div style={{ padding: 20 }} className="stack" data-testid="storage">
      <ErrorBanner error={error} />
      {now ? (
        <div className="grid tiles">
          <div className="card tile"><div className="label">Data files</div><div className="value">{now.dataFiles}</div>
            <div className="note">{size(now.bytes)}{now.dataFiles ? `, ${size(now.bytes / now.dataFiles)} each on average` : ""}</div></div>
          <div className="card tile"><div className="label">Rows</div><div className="value">{now.rows.toLocaleString()}</div>
            <div className="note">{now.deleteFiles ? `${now.deleteFiles} delete files` : "last changed " + ago(when(now.lastChange))}</div></div>
        </div>
      ) : <p className="muted">The catalog has no snapshot of this table yet.</p>}
      {!d.enabled ? <p className="muted small">Table maintenance isn't set up on this install.</p> : (
        <>
          <p className="small">
            {d.paused ? "Maintenance is paused for this project's tables." : <>Each night at {d.at}, the project's small files are compacted into larger ones.</>}
            {d.keepHistory && <> The data as it was goes back {duration(d.keepHistory)} (tags, such as a month-end close, are kept however old).</>}
          </p>
          {run ? (
            <div className={`banner ${run.error ? "warn" : "info"}`} data-testid="last-run">
              <strong>{run.error ? "Its last maintenance failed" : "Last kept"} {ago(when(run.ended))}</strong>{" "}
              {run.error ? <span className="mono small">{run.error}</span> :
                <span className="small">{counts(run.before)} → {counts(run.after)}, in {Math.max(1, Math.round((when(run.ended)!.getTime() - when(run.started)!.getTime()) / 1000))}s</span>}
            </div>
          ) : <p className="muted small">Not kept since the platform last started.</p>}
          {canRun && !d.paused && (
            <div className="row">
              <button className="btn" onClick={ask} disabled={d.state !== "idle"} data-testid="run-maintenance">
                {d.state === "running" ? "Keeping the tables…" : d.state === "queued" ? "Waiting its turn…" : "Run now"}
              </button>
              <span className="muted small">All of the project's tables, one at a time.</span>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function Preview({ project, table }: { project: string; table: string }) {
  const [answer, setAnswer] = useState<SQLAnswer>();
  const [error, setError] = useState<string>();
  useEffect(() => {
    runSQL(project, `SELECT * FROM "${table}" LIMIT 20`, 20).then(setAnswer).catch((e) => setError(e.message));
  }, [project, table]);
  if (error) return <div style={{ padding: 20 }}><ErrorBanner error={error} /></div>;
  if (!answer) return <div style={{ padding: 20 }}><Spinner /></div>;
  return <Results answer={answer} testid="preview" note="The first 20 rows, as you see them." />;
}

export function Results({ answer, testid, note }: { answer: SQLAnswer; testid: string; note?: string }) {
  return (
    <div>
      <div className="scroll-x">
        <table className="t data" data-testid={testid}>
          <thead><tr>{answer.columns.map((c) => <th key={c}>{c}</th>)}</tr></thead>
          <tbody>
            {answer.rows.map((r, i) => <tr key={i}>{r.map((v, j) => <td key={j}>{v === null ? <span className="muted">null</span> : String(v)}</td>)}</tr>)}
          </tbody>
        </table>
      </div>
      <p className="small muted" style={{ padding: "8px 16px", margin: 0, borderTop: "1px solid var(--line-soft)" }}>
        {answer.rows.length} {answer.rows.length === 1 ? "row" : "rows"}{answer.seconds !== undefined ? ` in ${answer.seconds}s` : ""}. {note}
      </p>
    </div>
  );
}
