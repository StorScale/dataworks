import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { asOf, atCommit, getFlow, Moment, runSQL, sourceCatalog, SQLAnswer } from "../api";
import { ErrorBanner, limits, maskLabel, usePoll, useProject } from "../lib";
import { Results } from "./Data";

// SQL is a query editor over the project's tables. Queries run in Trino as
// you, so a reader's row filters and masks apply here as everywhere.
export default function SQL() {
  const { name, view } = useProject();
  const spec = view?.spec?.spec;
  const [params] = useSearchParams();
  const flow = usePoll(() => getFlow(name), [name]);
  const starter = (t: string) => `SELECT *\nFROM ${t}\nLIMIT 100`;
  const [sql, setSql] = useState(params.get("sql") ?? (params.get("table") ? starter(params.get("table")!) : ""));
  const [at, setAt] = useState("");
  const [moment, setMoment] = useState<Moment>();
  const [answer, setAnswer] = useState<SQLAnswer>();
  const [error, setError] = useState<string>();
  const [running, setRunning] = useState(false);
  const [open, setOpen] = useState<string | undefined>(params.get("table") ?? undefined);
  const box = useRef<HTMLTextAreaElement>(null);
  const tables = flow.data?.tables.filter((t) => !t.external) ?? [];
  const reader = view?.role === "reader";
  useEffect(() => { if (!sql && tables[0]) setSql(starter(tables[0].name)); }, [tables[0]?.name]); // eslint-disable-line react-hooks/exhaustive-deps

  // The project's sources' tables, as Trino shows them to this person (what
  // Ranger lets them see): a source they may not read isn't listed.
  const catalogs = (spec?.sources ?? []).map((s) => sourceCatalog(name, s));
  const [sourceTables, setSourceTables] = useState<Record<string, SourceTable[]>>({});
  useEffect(() => {
    let live = true;
    catalogs.forEach((cat) => {
      runSQL(name, `SELECT table_schema, table_name, column_name, data_type FROM "${cat}".information_schema.columns
        WHERE table_schema <> 'information_schema' ORDER BY table_schema, table_name, ordinal_position`, 5000)
        .then((a) => { if (live) setSourceTables((st) => ({ ...st, [cat]: sourceTablesOf(a) })); })
        .catch(() => undefined);
    });
    return () => { live = false; };
  }, [name, catalogs.join(",")]); // eslint-disable-line react-hooks/exhaustive-deps

  const run = async () => {
    setRunning(true);
    setError(undefined);
    try {
      setAnswer(await runSQL(name, sql));
    } catch (e) {
      setError((e as Error).message);
      setAnswer(undefined);
    } finally {
      setRunning(false);
    }
  };
  const insert = (text: string) => {
    const el = box.current;
    if (!el) return setSql((s) => s + text);
    const [a, b] = [el.selectionStart, el.selectionEnd];
    setSql(sql.slice(0, a) + text + sql.slice(b));
    requestAnimationFrame(() => { el.focus(); el.setSelectionRange(a + text.length, a + text.length); });
  };
  const lines = sql.split("\n").length;
  const names = tables.map((t) => t.name);
  // As of a moment: the project's tables in the query named at its commit.
  const readAsOf = async () => {
    setError(undefined);
    if (!at.trim() || at.trim() === "now") {
      setMoment(undefined);
      setSql(atCommit(sql, names, ""));
      return;
    }
    try {
      const m = await asOf(name, at.trim());
      setMoment(m);
      setSql(atCommit(sql, names, m.commit));
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <div className="page">
      <div className="split">
        <nav className="side side-list" aria-label="Schema" data-testid="schema">
          <p className="eyebrow">{spec?.tables?.namespace ?? name.replace(/-/g, "_")}</p>
          {tables.map((t) => {
            const lim = limits(spec, t.name);
            return (
              <div key={t.fqn}>
                <button onClick={() => setOpen(open === t.name ? undefined : t.name)} onDoubleClick={() => insert(t.name)} aria-expanded={open === t.name} title="Double-click to insert">
                  <span className="mono">{t.name}</span><span className="muted small">{open === t.name ? "▾" : "▸"}</span>
                </button>
                {open === t.name && (
                  <div style={{ padding: "0 0 6px 12px" }}>
                    {t.columns.map((c) => (
                      <button key={c.name} onClick={() => insert(c.name)} style={{ minHeight: 30, padding: "4px 10px" }} title="Insert">
                        <span className="mono small">{c.name}</span>
                        <span className="small muted">{lim.masks[c.name] && reader ? <span className="pill amber">{maskLabel(lim.masks[c.name])}</span> : c.type}</span>
                      </button>
                    ))}
                  </div>
                )}
              </div>
            );
          })}
          {flow.data && tables.length === 0 && <p className="small muted" style={{ padding: 10 }}>No tables yet.</p>}
          {catalogs.filter((cat) => sourceTables[cat]?.length).map((cat) => (
            <div key={cat} data-testid="source-schema">
              <p className="eyebrow" style={{ marginTop: 12 }} title="A source: the database, queried where it is (read-only)">{cat}</p>
              {sourceTables[cat].map((t) => {
                const fqn = `${cat}.${t.schema}.${t.name}`;
                return (
                  <div key={fqn}>
                    <button onClick={() => setOpen(open === fqn ? undefined : fqn)} onDoubleClick={() => insert(fqn)} aria-expanded={open === fqn} title="Double-click to insert">
                      <span className="mono">{t.schema}.{t.name}</span><span className="muted small">{open === fqn ? "▾" : "▸"}</span>
                    </button>
                    {open === fqn && (
                      <div style={{ padding: "0 0 6px 12px" }}>
                        {t.columns.map((c) => (
                          <button key={c.name} onClick={() => insert(c.name)} style={{ minHeight: 30, padding: "4px 10px" }} title="Insert">
                            <span className="mono small">{c.name}</span><span className="small muted">{c.type}</span>
                          </button>
                        ))}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          ))}
        </nav>

        <section className="main stack">
          <div className="card flush">
            <div className="row" style={{ padding: "10px 16px", borderBottom: "1px solid var(--line-soft)" }}>
              <strong>Query</strong>
              <span className="small muted">runs in Trino as you{reader ? ", with readers' filters and masks" : ""}</span>
              <span className="grow" />
              <Link className="small" to="/tools/sql">Open SQL Lab</Link>
              <button className="btn primary" onClick={run} disabled={running || !sql.trim()} data-testid="run-sql">{running ? "Running…" : "Run"}</button>
            </div>
            <div className="editor">
              <div className="gutter" aria-hidden>{Array.from({ length: lines }, (_, i) => i + 1).join("\n")}</div>
              <textarea
                ref={box}
                value={sql}
                rows={Math.max(6, lines + 1)}
                spellCheck={false}
                aria-label="SQL"
                onChange={(e) => setSql(e.target.value)}
                onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); run(); } }}
                data-testid="sql"
              />
            </div>
            <div className="small muted" style={{ padding: "6px 16px", borderTop: "1px solid var(--line-soft)" }}>
              ⌘/Ctrl + Enter runs it. One statement at a time; the project's own table names are enough.
            </div>
            <form className="row" style={{ padding: "8px 16px", borderTop: "1px solid var(--line-soft)", gap: 8, flexWrap: "wrap" }}
              onSubmit={(e) => { e.preventDefault(); readAsOf(); }} data-testid="as-of">
              <label className="row small" style={{ flexDirection: "row", gap: 6 }}>As of
                <input value={at} onChange={(e) => setAt(e.target.value)} placeholder="now, a tag, 2026-09-30, a commit" style={{ width: 260 }} aria-label="As of" />
              </label>
              <button className="btn" type="submit">Read the tables then</button>
              {moment && <span className="small muted">as of {moment.label}: commit <span className="mono">{moment.commit.slice(0, 12)}</span>{moment.time ? `, ${new Date(moment.time).toLocaleString()}` : ""}</span>}
            </form>
          </div>
          <ErrorBanner error={error} />
          {answer && <div className="card flush"><Results answer={answer} testid="sql-result" /></div>}
        </section>
      </div>
    </div>
  );
}

type SourceTable = { schema: string; name: string; columns: { name: string; type: string }[] };

// sourceTablesOf groups information_schema.columns' rows (schema, table, column, type) by table.
function sourceTablesOf(a: SQLAnswer): SourceTable[] {
  const out: SourceTable[] = [];
  for (const [schema, table, column, type] of a.rows as string[][]) {
    let t = out[out.length - 1];
    if (!t || t.schema !== schema || t.name !== table) out.push((t = { schema, name: table, columns: [] }));
    t.columns.push({ name: column, type });
  }
  return out;
}
