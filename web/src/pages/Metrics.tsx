import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import { getSemantic, MetricAnswer, putSemantic, queryMetrics, runSemanticTests, type SemanticTest } from "../api";
import { ErrorBanner, Spinner, usePoll, useProject } from "../lib";

// Metrics is a project's semantic layer: its metrics, defined once, a way to
// ask for them, how agents use them, and (for editors) their definitions.
export default function Metrics() {
  const { name, view } = useProject();
  const [params] = useSearchParams();
  const model = usePoll(() => getSemantic(name), [name]);
  const [picked, setPicked] = useState<string[]>(params.get("metric") ? [params.get("metric")!] : []);
  const [groupBy, setGroupBy] = useState<string[]>([]);
  const [answer, setAnswer] = useState<MetricAnswer>();
  const [asking, setAsking] = useState(false);
  const [askError, setAskError] = useState<string>();
  const [draft, setDraft] = useState<string>();
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string>();
  // The model's tests (12.3): run as you, each answer checked against what it expects.
  const [tests, setTests] = useState<{ tests: SemanticTest[]; passed: number; failed: number }>();
  const [testing, setTesting] = useState(false);
  const [testError, setTestError] = useState<string>();
  const runTests = async () => {
    setTesting(true);
    setTestError(undefined);
    try { setTests(await runSemanticTests(name)); } catch (e) { setTestError((e as Error).message); } finally { setTesting(false); }
  };
  const [defsOpen, setDefsOpen] = useState(false);
  const editor = view?.role === "editor";

  if (model.error && !model.data) return <div className="page"><ErrorBanner error={model.error} /></div>;
  if (!model.data) return <div className="page"><Spinner /></div>;
  const m = model.data;
  const dims = [...new Set(m.metrics.filter((x) => picked.includes(x.name)).flatMap((x) => x.dimensions))].sort();
  const toggle = (list: string[], set: (v: string[]) => void, v: string) => set(list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  const ask = async () => {
    setAsking(true);
    setAskError(undefined);
    try {
      setAnswer(await queryMetrics(name, { metrics: picked, group_by: groupBy.filter((g) => dims.includes(g)), limit: 100 }));
    } catch (e) {
      setAskError((e as Error).message);
    } finally {
      setAsking(false);
    }
  };
  const save = async () => {
    setSaving(true);
    setSaveError(undefined);
    try {
      await putSemantic(name, draft ?? m.yaml);
      setDraft(undefined);
      model.reload();
    } catch (e) {
      setSaveError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };
  const mcpUrl = `${location.origin}/mcp`;

  return (
    <div className="page">
      {m.error && <div className="banner error" style={{ marginBottom: 16 }}>The definitions don't compile: {m.error}</div>}
      <div className="grid main-side">
        <div className="stack">
          <section className="card">
            <div className="card-head">
              <h2>Ask for metrics</h2>
              <span className="small muted">runs as you, so your filters and masks apply</span>
            </div>
            {m.metrics.length === 0 ? <p className="muted">No metrics yet.{editor ? " Define some below." : ""}</p> : (
              <>
                <p className="eyebrow" style={{ marginTop: 4 }}>Metrics</p>
                <div className="chips" data-testid="metric-chips">
                  {m.metrics.map((x) => (
                    <button key={x.name} className={`chip${picked.includes(x.name) ? " on" : ""}`} aria-pressed={picked.includes(x.name)} title={x.description}
                      onClick={() => toggle(picked, setPicked, x.name)} data-testid={`metric-${x.name}`}>{x.name}</button>
                  ))}
                </div>
                {dims.length > 0 && (
                  <>
                    <p className="eyebrow" style={{ marginTop: 16 }}>Group by</p>
                    <div className="chips">
                      {dims.map((d) => (
                        <button key={d} className={`chip soft${groupBy.includes(d) ? " on" : ""}`} aria-pressed={groupBy.includes(d)} onClick={() => toggle(groupBy, setGroupBy, d)} data-testid={`dim-${d}`}>{d}</button>
                      ))}
                    </div>
                  </>
                )}
                <div className="row" style={{ marginTop: 16 }}>
                  <button className="btn primary" onClick={ask} disabled={asking || picked.length === 0} data-testid="ask">{asking ? "Asking…" : "Ask"}</button>
                  {picked.length === 0 && <span className="small muted">Pick a metric.</span>}
                </div>
              </>
            )}
            <ErrorBanner error={askError} />
          </section>

          {answer?.rows && <Answer answer={answer} />}

          <section className="card flush">
            <div className="card-head" style={{ padding: "16px 20px 0" }}>
              <h2>The metrics</h2>
              <span className="small muted">over {m.tables.join(", ") || "no tables yet"}</span>
            </div>
            <table className="t" data-testid="metrics">
              <thead><tr><th>Metric</th><th>What it is</th><th>Group by</th></tr></thead>
              <tbody>
                {m.metrics.map((x) => (
                  <tr key={x.name}>
                    <td><span className="mono">{x.name}</span> <span className="pill">{x.type}</span></td>
                    <td>{x.description}</td>
                    <td className="small muted mono">{x.dimensions.join(", ")}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>

          <section className="card">
            <div className="card-head">
              <h2>Definitions</h2>
              <button className="linkish" onClick={() => setDefsOpen(!defsOpen)} aria-expanded={defsOpen} data-testid="definitions-toggle">{defsOpen ? "Hide" : editor ? "Edit" : "Show"}</button>
              {(m.tests?.length ?? 0) > 0 && <button className="btn small" style={{ marginLeft: 8 }} onClick={runTests} disabled={testing} data-testid="run-tests">{testing ? "Running…" : `Run the ${m.tests!.length} tests`}</button>}
            </div>
            <p className="small muted" style={{ margin: 0 }}>
              MetricFlow's YAML: semantic models over the project's tables, and the metrics made from them.{" "}
              {editor ? "Changes apply to the next question anyone asks." : "Only the project's editors change them."}
            </p>
            {defsOpen && (
              <div style={{ marginTop: 12 }}>
                {testError && <ErrorBanner error={testError} />}
                {tests && (
                  <ul className="small" data-testid="test-results" style={{ margin: "0 0 10px", paddingLeft: 18 }}>
                    {tests.tests.map((t) => (
                      <li key={t.name}>{t.passed ? "✓" : "✗"} <span className="mono">{t.name}</span>{t.why ? `: ${t.why}` : ""}{t.rows && t.passed ? ` (${t.rows.length} rows)` : ""}</li>
                    ))}
                    {tests.tests.length === 0 && <li className="muted">No tests in the model.</li>}
                  </ul>
                )}
                <textarea className="yaml" spellCheck={false} value={draft ?? m.yaml} readOnly={!editor} onChange={(e) => setDraft(e.target.value)} aria-label="Definitions" data-testid="semantic-yaml" />
                <ErrorBanner error={saveError} />
                {editor && (
                  <div className="form-actions" style={{ margin: "12px 0 0" }}>
                    {draft !== undefined && <button className="btn" onClick={() => setDraft(undefined)}>Discard</button>}
                    <button className="btn primary" onClick={save} disabled={saving || draft === undefined} data-testid="save-semantic">{saving ? "Checking…" : "Save"}</button>
                  </div>
                )}
              </div>
            )}
          </section>
        </div>

        <aside className="stack">
          <section className="card" data-testid="agents">
            <h2>Agents</h2>
            <p className="small">
              Agents ask for these metrics over MCP, signed in through Keycloak as the person using them. Each question runs as that person,
              and Ranger's audit log names the agent too.
            </p>
            <p className="eyebrow">Add to Claude Code</p>
            <pre className="code dark">claude mcp add --transport http storscale {mcpUrl}</pre>
            <p className="eyebrow" style={{ marginTop: 14 }}>Tools</p>
            <div className="list small">
              <div><code>list_projects</code> <span className="muted">your projects</span></div>
              <div><code>list_metrics</code> <span className="muted">a project's metrics</span></div>
              <div><code>query_metrics</code> <span className="muted">ask, as you</span></div>
              <div><code>explain_query</code> <span className="muted">the SQL, without running it</span></div>
            </div>
            <p className="small muted" style={{ marginBottom: 0 }}>Keycloak client <code>storscale-agent</code>.</p>
          </section>
        </aside>
      </div>
    </div>
  );
}

function Answer({ answer }: { answer: MetricAnswer }) {
  const cols = answer.columns ?? [];
  const rows = answer.rows ?? [];
  // Trino's decimals arrive as strings: "449.74".
  const num = (v: unknown) => typeof v === "number" ? v : typeof v === "string" && /^-?\d+(\.\d+)?$/.test(v) ? Number(v) : undefined;
  // One label column and one number column: a bar for each row.
  const chartable = cols.length === 2 && rows.length > 1 && rows.length <= 20 && rows.every((r) => num(r[1]) !== undefined);
  const max = chartable ? Math.max(...rows.map((r) => Math.abs(num(r[1])!)), 1) : 1;
  const fmt = (v: unknown) => typeof v === "number" ? v.toLocaleString(undefined, { maximumFractionDigits: 2 }) : v === null ? "null" : String(v);
  return (
    <section className="card" data-testid="answer-card">
      <div className="card-head"><h2>Answer</h2><span className="small muted">{rows.length} {rows.length === 1 ? "row" : "rows"}</span></div>
      {chartable && (
        <div className="bars" style={{ marginBottom: 16 }} aria-hidden>
          {rows.map((r, i) => (
            <div key={i} style={{ display: "contents" }}>
              <span className="mono small">{String(r[0] ?? "null")}</span>
              <div><div className="bar" style={{ width: `${(Math.abs(num(r[1])!) / max) * 100}%` }} /></div>
              <span className="v small">{fmt(r[1])}</span>
            </div>
          ))}
        </div>
      )}
      <div className="scroll-x" style={{ border: "1px solid var(--line-soft)", borderRadius: 8 }}>
        <table className="t data" data-testid="answer">
          <thead><tr>{cols.map((c) => <th key={c}>{c}</th>)}</tr></thead>
          <tbody>{rows.map((r, i) => <tr key={i}>{r.map((v, j) => <td key={j}>{fmt(v)}</td>)}</tr>)}</tbody>
        </table>
      </div>
      <details style={{ marginTop: 12 }}>
        <summary className="small" style={{ cursor: "pointer" }}>The SQL it ran, as you</summary>
        <pre className="code" style={{ marginTop: 8 }}>{answer.sql}</pre>
      </details>
    </section>
  );
}
