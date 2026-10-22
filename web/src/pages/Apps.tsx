import { useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { appData, askApp, deleteApp, getApps, getSemantic, putApp, type App, type AppFilter, type KnowledgeHit } from "../api";
import { Empty, Intro } from "../components";
import { ErrorBanner, Spinner, usePoll, useProject } from "../lib";

// A project's apps (use case 14.1): small pages of its data, declared by its
// editors (a metric by a dimension, a table's columns, or questions answered
// from a knowledge bank) and opened by its members. Every app's data is
// fetched as whoever opens it, so each sees what their rules let them see.

const KINDS: [App["kind"], string][] = [["metric", "Metrics, by a dimension"], ["table", "A table's rows"], ["qa", "Questions, answered from a knowledge bank"]];
const SHOWS: [NonNullable<App["show"]>, string][] = [["bar", "Bars"], ["line", "A line"], ["number", "One number"], ["table", "A table"]];

export default function Apps() {
  const { name, view } = useProject();
  const list = usePoll(() => getApps(name), [name], 30_000);
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<string>();
  if (list.error && !list.data) return <div className="page"><ErrorBanner error={list.error} /></div>;
  if (!list.data) return <div className="page"><Spinner /></div>;
  const d = list.data;
  const remove = async (a: string) => {
    if (!window.confirm(`Take the app ${a} out of the project?`)) return;
    setError(undefined);
    try { await deleteApp(name, a); list.reload(); } catch (e) { setError((e as Error).message); }
  };
  return (
    <div className="page stack" data-testid="apps">
      <Intro actions={d.canEdit && !adding && <button className="btn primary" onClick={() => setAdding(true)} data-testid="add-app">Make an app</button>}>
          Small pages of the project's data for the people who read it: a metric by a dimension, a table's rows, or questions answered from its documents.
          Each shows whoever opens it what their rules let them see.
      </Intro>
      <ErrorBanner error={error} />
      {adding && <AppForm project={name} banks={d.banks ?? []} hasBudget={d.hasBudget} taken={d.apps.map((a) => a.name)} onDone={() => { setAdding(false); list.reload(); }} />}
      {d.apps.length === 0 && !adding ? <Empty>No apps yet.{d.canEdit ? " Make one from a metric, a table or a knowledge bank." : ""}</Empty> : (
        <div className="grid cards">
          {d.apps.map((a) => (
            <div key={a.name} className="card stack" style={{ gap: 6 }} data-testid={`app-${a.name}`}>
              <h2><Link to={`/p/${name}/apps/${a.name}`}>{a.title || a.name}</Link></h2>
              <span className="small muted">{a.description || KINDS.find(([k]) => k === a.kind)?.[1]}</span>
              <span className="small muted mono">{a.kind === "metric" ? `${(a.metrics ?? []).join(", ")}${a.by ? ` by ${a.by}` : ""}` : a.kind === "table" ? a.table : `bank ${a.bank}`}</span>
              {(view?.role === "editor") && <div style={{ marginTop: 6 }}><button className="btn small danger" onClick={() => remove(a.name)}>Remove</button></div>}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// The form an editor makes an app with: what it's of, how it shows, and its filters.
function AppForm({ project, banks, hasBudget, taken, onDone }: { project: string; banks: string[]; hasBudget: boolean; taken: string[]; onDone: () => void }) {
  const model = usePoll(() => getSemantic(project), [project], 0);
  const [a, setA] = useState<App>({ name: "", kind: "metric", metrics: [], show: "bar", filters: [] });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const set = (patch: Partial<App>) => setA({ ...a, ...patch });
  const metrics = model.data?.metrics ?? [];
  const dims = useMemo(() => [...new Set(metrics.filter((m) => (a.metrics ?? []).includes(m.name)).flatMap((m) => m.dimensions))].sort(), [metrics, a.metrics]);
  const ok = /^[a-z][a-z0-9-]{0,40}$/.test(a.name) && !taken.includes(a.name) &&
    (a.kind === "metric" ? (a.metrics ?? []).length > 0 : a.kind === "table" ? !!a.table : !!a.bank);
  const save = async () => {
    setBusy(true); setError(undefined);
    const clean: App = { ...a, filters: (a.filters ?? []).filter((f) => f.field) };
    if (clean.kind !== "metric") { delete clean.metrics; delete clean.by; }
    if (clean.kind === "table") clean.show = "table";
    if (clean.kind === "qa") { delete clean.show; clean.filters = []; }
    try { await putApp(project, clean); onDone(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  const setFilter = (i: number, f: AppFilter) => set({ filters: (a.filters ?? []).map((x, j) => (j === i ? f : x)) });
  return (
    <form className="card stack" onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="app-form">
      <h2>Make an app</h2>
      <ErrorBanner error={error ?? model.error} />
      <div className="fields">
        <label>Name<input className="mono" value={a.name} onChange={(e) => set({ name: e.target.value })} placeholder="revenue-by-region" data-testid="app-name" /></label>
        <label className="grow">Title<input value={a.title ?? ""} onChange={(e) => set({ title: e.target.value || undefined })} placeholder="Revenue by region" /></label>
      </div>
      <label>What it's for<input value={a.description ?? ""} onChange={(e) => set({ description: e.target.value || undefined })} placeholder="This quarter's revenue, by region, for the regional leads" /></label>
      <label>What it shows<select value={a.kind} onChange={(e) => set({ kind: e.target.value as App["kind"] })} data-testid="app-kind">
        {KINDS.map(([k, l]) => <option key={k} value={k} disabled={k === "qa" && (!banks.length || !hasBudget)}>{l}{k === "qa" && !banks.length ? " (the project has no knowledge bank)" : ""}</option>)}</select></label>
      {a.kind === "metric" && <>
        <div className="field-label">Metrics <span className="field-help">each its own chart, on its own scale</span></div>
        <div className="chips">{metrics.map((m) => <button key={m.name} type="button" className={`chip${(a.metrics ?? []).includes(m.name) ? " on" : ""}`} aria-pressed={(a.metrics ?? []).includes(m.name)}
          onClick={() => set({ metrics: (a.metrics ?? []).includes(m.name) ? (a.metrics ?? []).filter((x) => x !== m.name) : [...(a.metrics ?? []), m.name] })}>{m.name}</button>)}
          {model.data && metrics.length === 0 && <span className="small muted">The project has no metrics yet (the Metrics tab).</span>}</div>
        <div className="fields">
          <label>By<select value={a.by ?? ""} onChange={(e) => set({ by: e.target.value || undefined })} data-testid="app-by">
            <option value="">nothing (the total)</option>{dims.map((d) => <option key={d}>{d}</option>)}</select></label>
          <label>As<select value={a.show} onChange={(e) => set({ show: e.target.value as App["show"] })}>{SHOWS.map(([k, l]) => <option key={k} value={k}>{l}</option>)}</select></label>
        </div>
      </>}
      {a.kind === "table" && <div className="fields">
        <label>Table<select value={a.table ?? ""} onChange={(e) => set({ table: e.target.value || undefined })} data-testid="app-table">
          <option value="">table…</option>{(model.data?.tables ?? []).map((t) => <option key={t}>{t}</option>)}</select></label>
        <label className="grow">Columns (none: all)<input className="mono" value={(a.columns ?? []).join(", ")}
          onChange={(e) => set({ columns: e.target.value.split(",").map((c) => c.trim()).filter(Boolean) })} placeholder="order_id, region, amount" /></label>
      </div>}
      {a.kind === "qa" && <label>Knowledge bank<select value={a.bank ?? ""} onChange={(e) => set({ bank: e.target.value || undefined })} data-testid="app-bank">
        <option value="">bank…</option>{banks.map((b) => <option key={b}>{b}</option>)}</select>
        <span className="field-help">A question is answered by the model from the bank's closest passages alone, citing them, as the person asking; on the project's key.</span></label>}
      {a.kind !== "qa" && <>
        <div className="field-label">Filters <span className="field-help">the viewer picks: a {a.kind === "metric" ? "dimension" : "column"} equal to a value, from a list or typed</span></div>
        {(a.filters ?? []).map((f, i) => (
          <div key={i} className="fields">
            {a.kind === "metric"
              ? <select value={f.field} onChange={(e) => setFilter(i, { ...f, field: e.target.value })} aria-label="Dimension"><option value="">dimension…</option>{dims.map((d) => <option key={d}>{d}</option>)}</select>
              : <input className="mono" value={f.field} onChange={(e) => setFilter(i, { ...f, field: e.target.value })} placeholder="column" aria-label="Column" />}
            <input value={f.label ?? ""} onChange={(e) => setFilter(i, { ...f, label: e.target.value || undefined })} placeholder="label: Region" aria-label="Label" />
            <input className="grow" value={(f.values ?? []).join(", ")} onChange={(e) => setFilter(i, { ...f, values: e.target.value.split(",").map((v) => v.trim()).filter(Boolean) })}
              placeholder="values (none: typed): EU, US, APAC" aria-label="Values" />
            <button type="button" className="btn" onClick={() => set({ filters: (a.filters ?? []).filter((_, j) => j !== i) })}>Remove</button>
          </div>
        ))}
        <div><button type="button" className="btn small" onClick={() => set({ filters: [...(a.filters ?? []), { field: "" }] })}>Add a filter</button></div>
      </>}
      <div className="form-actions">
        <button className="btn primary" type="submit" disabled={!ok || busy} data-testid="save-app">{busy ? "Saving…" : "Make it"}</button>
        <button className="btn" type="button" onClick={onDone}>Cancel</button>
      </div>
    </form>
  );
}

// An app, as whoever opens it.
export function AppPage() {
  const { name } = useProject();
  const { app = "" } = useParams();
  const list = usePoll(() => getApps(name), [name], 0);
  const a = list.data?.apps.find((x) => x.name === app);
  if (list.error && !list.data) return <div className="page"><ErrorBanner error={list.error} /></div>;
  if (!list.data) return <div className="page"><Spinner /></div>;
  if (!a) return <div className="page"><Empty>No app {app}. <Link to={`/p/${name}/apps`}>The project's apps</Link></Empty></div>;
  return (
    <div className="page stack" data-testid={`app-page-${a.name}`}>
      <div><Link to={`/p/${name}/apps`} className="small">← Apps</Link></div>
      <div>
        <h1>{a.title || a.name}</h1>
        {a.description && <p className="lead">{a.description}</p>}
      </div>
      {a.kind === "qa" ? <Questions project={name} app={a} /> : <AppData project={name} app={a} />}
    </div>
  );
}

function AppData({ project, app }: { project: string; app: App }) {
  const [picked, setPicked] = useState<Record<string, string>>({});
  const [applied, setApplied] = useState<Record<string, string>>({});
  const [asTable, setAsTable] = useState(app.show === "table" || app.kind === "table");
  const data = usePoll(() => appData(project, app.name, applied), [project, app.name, JSON.stringify(applied)], 0);
  const cols = data.data?.columns ?? [];
  const rows = data.data?.rows ?? [];
  return (
    <>
      {(app.filters ?? []).length > 0 && (
        <form className="fields" onSubmit={(e) => { e.preventDefault(); setApplied({ ...picked }); }} data-testid="app-filters">
          {(app.filters ?? []).map((f) => (
            <label key={f.field}>{f.label || f.field}
              {f.values?.length
                ? <select value={picked[f.field] ?? ""} onChange={(e) => setPicked({ ...picked, [f.field]: e.target.value })}><option value="">all</option>{f.values.map((v) => <option key={v}>{v}</option>)}</select>
                : <input value={picked[f.field] ?? ""} onChange={(e) => setPicked({ ...picked, [f.field]: e.target.value })} placeholder="all" style={{ width: 160 }} />}
            </label>))}
          <button className="btn" type="submit">Show</button>
        </form>)}
      <ErrorBanner error={data.error} />
      {!data.data ? (!data.error && <Spinner />) : rows.length === 0 ? <Empty>Nothing to show{Object.keys(applied).length ? " for these filters" : ""}.</Empty> : (
        <div className="card stack" style={{ gap: 8 }}>
          {app.kind === "metric" && app.show !== "table" && (
            <div className="row" style={{ justifyContent: "flex-end" }}><button className="btn small" onClick={() => setAsTable(!asTable)}>{asTable ? "As a chart" : "As a table"}</button></div>)}
          {asTable ? <Rows cols={cols} rows={rows} /> : app.show === "number"
            ? <div data-testid="app-number"><div className="hero-number">{fmt(rows[0]?.[cols.length - 1])}</div><div className="small muted">{(app.metrics ?? [])[0]}</div></div>
            : (app.metrics ?? []).map((m, i) => {
              const at = cols.indexOf(m) >= 0 ? cols.indexOf(m) : i + 1;
              const points = rows.map((r) => ({ label: String(r[0] ?? ""), value: Number(r[at] ?? 0) }));
              return <div key={m}><h2>{m}{app.by ? <span className="muted" style={{ fontWeight: 400 }}> by {app.by}</span> : null}</h2>
                {app.show === "line" ? <LineChart points={points} /> : <BarChart points={points} />}</div>;
            })}
        </div>)}
    </>
  );
}

function Rows({ cols, rows }: { cols: string[]; rows: unknown[][] }) {
  return (
    <div className="scroll-x"><table className="t" data-testid="app-rows">
      <thead><tr>{cols.map((c) => <th key={c}>{c}</th>)}</tr></thead>
      <tbody>{rows.map((r, i) => <tr key={i}>{r.map((v, j) => <td key={j} className={typeof v === "number" || (typeof v === "string" && numeric.test(v)) ? "mono" : ""}>{fmt(v)}</td>)}</tr>)}</tbody>
    </table></div>
  );
}

// A value as shown: a number (or a decimal the semantic layer sends as text, "10.0") to two places; text as it is
// (one with a leading zero, an ID or a postcode, stays text).
const numeric = /^-?(0|[1-9]\d*)(\.\d+)?([eE][-+]?\d+)?$/;
const fmt = (v: unknown) =>
  typeof v === "number" ? v.toLocaleString(undefined, { maximumFractionDigits: 2 })
    : typeof v === "string" && numeric.test(v) ? Number(v).toLocaleString(undefined, { maximumFractionDigits: 2 })
      : v == null ? "" : String(v);

// Bars, one series: a category's bar from the axis at 0, its value on hover (and at the bar's end when there are few).
function BarChart({ points }: { points: { label: string; value: number }[] }) {
  const [hover, setHover] = useState<number>();
  const W = 640, row = 26, left = 140, right = 70, H = points.length * row + 8;
  const max = Math.max(...points.map((p) => p.value), 0) || 1;
  const x = (v: number) => left + (Math.max(v, 0) / max) * (W - left - right);
  return (
    <div style={{ position: "relative" }}>
      <svg className="chart" viewBox={`0 0 ${W} ${H}`} role="img" aria-label={points.map((p) => `${p.label}: ${fmt(p.value)}`).join("; ")}>
        <line className="grid" x1={left} x2={left} y1={0} y2={H} />
        {points.map((p, i) => {
          const y = 4 + i * row, w = x(p.value) - left;
          return (
            <g key={i} onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(undefined)}>
              <rect className="hit" x={0} y={y} width={W} height={row} />
              <text className="axis" x={left - 8} y={y + row / 2 + 4} textAnchor="end">{p.label.length > 20 ? p.label.slice(0, 19) + "…" : p.label}</text>
              <path className={`bar${hover !== undefined && hover !== i ? " dim" : ""}`}
                d={w > 4 ? `M${left},${y + 4} h${w - 4} a4,4 0 0 1 4,4 v${row - 16} a4,4 0 0 1 -4,4 h${-(w - 4)} z` : `M${left},${y + 4} h${Math.max(w, 1)} v${row - 8} h${-Math.max(w, 1)} z`} />
              {(points.length <= 12 || hover === i) && <text className="axis" x={x(p.value) + 6} y={y + row / 2 + 4}>{fmt(p.value)}</text>}
            </g>);
        })}
      </svg>
    </div>
  );
}

// A line, one series over its categories (time, in order): a crosshair and the point's value on hover.
function LineChart({ points }: { points: { label: string; value: number }[] }) {
  const [hover, setHover] = useState<number>();
  const W = 640, H = 220, left = 48, right = 16, top = 12, bottom = 28;
  const vals = points.map((p) => p.value);
  const lo = Math.min(0, ...vals), hi = Math.max(...vals, 0) || 1;
  const x = (i: number) => left + (points.length > 1 ? (i / (points.length - 1)) * (W - left - right) : (W - left - right) / 2);
  const y = (v: number) => top + (1 - (v - lo) / (hi - lo || 1)) * (H - top - bottom);
  const ticks = [lo, (lo + hi) / 2, hi];
  const every = Math.max(1, Math.ceil(points.length / 8));
  return (
    <div style={{ position: "relative" }}>
      <svg className="chart" viewBox={`0 0 ${W} ${H}`} role="img" aria-label={points.map((p) => `${p.label}: ${fmt(p.value)}`).join("; ")}
        onMouseLeave={() => setHover(undefined)}>
        {ticks.map((t, i) => <g key={i}><line className="grid" x1={left} x2={W - right} y1={y(t)} y2={y(t)} /><text className="axis" x={left - 6} y={y(t) + 4} textAnchor="end">{fmt(t)}</text></g>)}
        {points.map((p, i) => i % every === 0 && <text key={i} className="axis" x={x(i)} y={H - 8} textAnchor="middle">{p.label.slice(0, 10)}</text>)}
        <path className="line" d={points.map((p, i) => `${i ? "L" : "M"}${x(i)},${y(p.value)}`).join(" ")} />
        {hover !== undefined && <><line className="cross" x1={x(hover)} x2={x(hover)} y1={top} y2={H - bottom} /><circle className="dot" cx={x(hover)} cy={y(points[hover].value)} r={5} /></>}
        {points.map((_, i) => <rect key={i} className="hit" x={x(i) - (W - left - right) / Math.max(points.length - 1, 1) / 2} y={0}
          width={(W - left - right) / Math.max(points.length - 1, 1)} height={H} onMouseEnter={() => setHover(i)} />)}
      </svg>
      {hover !== undefined && <div className="chart-tip" style={{ left: `${(x(hover) / W) * 100}%`, top: `${(y(points[hover].value) / H) * 100}%` }}>
        {points[hover].label}: <strong>{fmt(points[hover].value)}</strong></div>}
    </div>
  );
}

// A question app: ask, and the answer with the passages it cites.
function Questions({ project, app }: { project: string; app: App }) {
  const [q, setQ] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [out, setOut] = useState<{ answer: string; passages: KnowledgeHit[] }>();
  const ask = async () => {
    setBusy(true); setError(undefined);
    try { setOut(await askApp(project, app.name, q)); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <div className="card stack" style={{ gap: 8 }} data-testid="app-qa">
      <form className="row" onSubmit={(e) => { e.preventDefault(); if (q.trim()) ask(); }}>
        <input className="grow" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Ask a question about what the project's documents say" data-testid="app-question" />
        <button className="btn primary" type="submit" disabled={!q.trim() || busy}>{busy ? "Asking…" : "Ask"}</button>
      </form>
      <ErrorBanner error={error} />
      {out && <>
        <div style={{ whiteSpace: "pre-wrap" }} data-testid="app-answer">{out.answer}</div>
        {out.passages.length > 0 && <div className="small muted">From:</div>}
        <ol className="small" style={{ margin: 0, paddingLeft: 20 }}>{out.passages.map((p, i) => <li key={i}><span className="mono">{p.source} {p.ref}</span>: {p.text.length > 240 ? p.text.slice(0, 240) + "…" : p.text}</li>)}</ol>
      </>}
    </div>
  );
}
