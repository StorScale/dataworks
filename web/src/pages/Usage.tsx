import { useState } from "react";
import { Link } from "react-router-dom";
import { getProjectUsage, getUsage, type UsageMonth } from "../api";
import { ErrorBanner, Spinner, usePoll, useProject } from "../lib";

// Usage and cost (use case 13.1): what a project's Trino time, storage and
// model calls cost this month, by the install's rate card, against its
// budget and last month; and what drives it. The project's editors see
// theirs; finance (the install's usage viewers) and administrators every
// project's.

const months = (): string[] => {
  const out: string[] = [];
  const d = new Date();
  for (let i = 0; i < 12; i++) {
    out.push(new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() - i, 1)).toISOString().slice(0, 7));
  }
  return out;
};
// to the cent, or, under a cent, to two significant digits: a small project's month is still something
const money = (x: number, cur: string) =>
  `${x !== 0 && Math.abs(x) < 0.01 ? x.toFixed(Math.min(20, 1 - Math.floor(Math.log10(Math.abs(x))))).replace(/0+$/, "") : x.toFixed(2)} ${cur}`;
const hours = (s: number) => (s / 3600 < 0.1 ? `${s.toFixed(1)} s` : `${(s / 3600).toFixed(2)} h`);
const bytes = (b: number) => (b >= 1e12 ? `${(b / 1e12).toFixed(2)} TB` : b >= 1e9 ? `${(b / 1e9).toFixed(2)} GB` : `${(b / 1e6).toFixed(1)} MB`);
const change = (now: number, before: number) => (before > 0 ? `${now >= before ? "+" : ""}${(((now - before) / before) * 100).toFixed(0)}% on last month` : "nothing last month");

function MonthPick({ month, setMonth }: { month: string; setMonth: (m: string) => void }) {
  return <select value={month} onChange={(e) => setMonth(e.target.value)} aria-label="Month">{months().map((m) => <option key={m}>{m}</option>)}</select>;
}

function Budget({ u }: { u: UsageMonth }) {
  if (!u.budget) return <span className="muted">no budget set</span>;
  const share = (u.share ?? 0) * 100;
  return <span className={`pill ${share >= 100 ? "red" : share >= 80 ? "amber" : ""}`}>{share.toFixed(0)}% of {money(u.budget, u.currency)}</span>;
}

export function ProjectUsage() {
  const { name } = useProject();
  const [month, setMonth] = useState(months()[0]);
  const { data, error } = usePoll(() => getProjectUsage(name, month), [name, month], 60_000);
  const u = data?.usage;
  return (
    <div className="page stack" data-testid="usage">
      <div className="row">
        <p className="lead grow" style={{ margin: 0 }}>What the project used and what it cost, by the install's rate card: its queries' CPU time, what it keeps, and its model calls.</p>
        <MonthPick month={month} setMonth={setMonth} />
      </div>
      <ErrorBanner error={error} />
      {!data || !u ? (!error && <Spinner />) : <>
        <div className="card stack" style={{ gap: 4 }} data-testid="usage-total">
          <div className="row"><strong style={{ fontSize: 20 }}>{money(u.cost.total, u.currency)}</strong><Budget u={u} />
            <span className="muted small">{change(u.cost.total, data.before.cost.total ?? 0)}</span></div>
          <table className="t">
            <thead><tr><th>What</th><th>Used</th><th>Cost</th><th>Rate</th></tr></thead>
            <tbody>
              <tr><td>Queries (Trino)</td><td>{u.trino.queries} queries, {hours(u.trino.cpuSeconds)} of CPU, {bytes(u.trino.readBytes)} read</td>
                <td>{money(u.cost.trino, u.currency)}</td><td className="small muted">{data.rates.cpuHour} a CPU-hour</td></tr>
              <tr><td>Storage</td><td>{u.storage ? `${bytes(Object.values(u.storage.tables).reduce((a, b) => a + b, 0))} in tables, ${bytes(u.storage.bucket)} in files` : "not sampled yet"}
                {u.storageAt && <span className="muted small"> (as of {u.storageAt}; {u.avgGB.toFixed(2)} GB on average)</span>}</td>
                <td>{money(u.cost.storage, u.currency)}</td><td className="small muted">{data.rates.storageGBMonth} a GB-month</td></tr>
              <tr><td>Model calls</td><td>through the gateway, on the project's and its agents' keys</td><td>{money(u.cost.llm, u.currency)}</td>
                <td className="small muted">as the gateway prices them</td></tr>
            </tbody>
          </table>
          <div className="small muted">Spark isn't counted yet. A budget is set on the project (cost.budget); at 80% and at 100% the project is alerted; nothing is stopped.</div>
        </div>
        <div className="card stack" style={{ gap: 4 }}>
          <strong>Who ran the queries</strong>
          {Object.keys(u.trino.byUser ?? {}).length === 0 ? <div className="small muted">No queries this month.</div> : (
            <table className="t" data-testid="usage-users">
              <thead><tr><th>Who</th><th>Queries</th><th>CPU</th><th>Cost</th></tr></thead>
              <tbody>{Object.entries(u.trino.byUser ?? {}).sort((a, b) => b[1].cpuSeconds - a[1].cpuSeconds).slice(0, 15).map(([who, x]) => (
                <tr key={who}><td className="mono small">{who}</td><td>{x.queries}</td><td>{hours(x.cpuSeconds)}</td>
                  <td>{money((x.cpuSeconds / 3600) * data.rates.cpuHour, u.currency)}</td></tr>))}</tbody>
            </table>)}
        </div>
        {(u.trino.top ?? []).length > 0 && (
          <div className="card stack" style={{ gap: 4 }}>
            <strong>The costliest queries</strong>
            <table className="t" data-testid="usage-top">
              <thead><tr><th>When</th><th>Who</th><th>CPU</th><th>Read</th><th>Query</th></tr></thead>
              <tbody>{(u.trino.top ?? []).map((q) => (
                <tr key={q.id}><td className="small">{new Date(q.at).toLocaleString()}</td><td className="mono small">{q.user}</td><td>{hours(q.cpuSeconds)}</td>
                  <td>{bytes(q.readBytes)}</td><td className="mono small" style={{ maxWidth: 480, overflowWrap: "anywhere" }}>{q.sql}</td></tr>))}</tbody>
            </table>
          </div>)}
        {u.storage && Object.keys(u.storage.tables).length > 0 && (
          <div className="card stack" style={{ gap: 4 }}>
            <strong>The largest tables</strong>
            <table className="t" data-testid="usage-tables">
              <thead><tr><th>Table</th><th>Size</th></tr></thead>
              <tbody>{Object.entries(u.storage.tables).sort((a, b) => b[1] - a[1]).slice(0, 15).map(([t, b]) => (
                <tr key={t}><td className="mono small">{t}</td><td>{bytes(b)}</td></tr>))}</tbody>
            </table>
          </div>)}
      </>}
    </div>
  );
}

export default function Usage() {
  const [month, setMonth] = useState(months()[0]);
  const { data, error } = usePoll(() => getUsage(month), [month], 60_000);
  return (
    <div className="page stack" data-testid="usage-all">
      <div className="row">
        <p className="lead grow" style={{ margin: 0 }}>What each project cost this month, by the install's rate card ({data ? `${data.rates.cpuHour} a CPU-hour, ${data.rates.storageGBMonth} a GB-month` : "…"}), against its budget and last month.</p>
        <MonthPick month={month} setMonth={setMonth} />
      </div>
      <ErrorBanner error={error} />
      {!data ? (!error && <Spinner />) : data.projects.length === 0 ? <div className="empty">No projects.</div> : (
        <table className="t" data-testid="usage-projects">
          <thead><tr><th>Project</th><th>Total</th><th>Queries</th><th>Storage</th><th>Model calls</th><th>Budget</th><th>Last month</th></tr></thead>
          <tbody>{data.projects.map((u) => (
            <tr key={u.project}>
              <td><Link to={`/p/${u.project}/usage`}>{u.project}</Link></td>
              <td><strong>{money(u.cost.total, u.currency)}</strong></td>
              <td>{money(u.cost.trino, u.currency)} <span className="muted small">{hours(u.trino.cpuSeconds)}</span></td>
              <td>{money(u.cost.storage, u.currency)} <span className="muted small">{u.avgGB.toFixed(1)} GB</span></td>
              <td>{money(u.cost.llm, u.currency)}</td>
              <td><Budget u={u} /></td>
              <td className="small">{money(u.before, u.currency)} <span className="muted">{change(u.cost.total, u.before)}</span></td>
            </tr>))}</tbody>
        </table>
      )}
    </div>
  );
}
