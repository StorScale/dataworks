import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ago, getPipelines, startRun, took, when, type ImportError, type PipelinesRepo } from "../api";
import { ErrorBanner, Spinner, StateDot, usePoll, useProject } from "../lib";

// Pipelines is a project's Airflow DAGs: those in its pipelines repo, and the
// platform's own tagged for it. Their recent runs, the newest run's tasks, the
// commit each ran, and (for editors) a way to start one. Writing them happens
// in the repo (or Airflow's own DAGs folder).

const short = (sha?: string) => sha?.slice(0, 7);

// Repo is the project's pipelines repo, and the files in it Airflow couldn't load.
function Repo({ repo, errors }: { repo: PipelinesRepo; errors?: ImportError[] }) {
  return (
    <div className="card" data-testid="pipelines-repo">
      <p className="small" style={{ margin: 0 }}>
        From <code>{repo.url}</code>{repo.path && <>, in <code>{repo.path}</code></>}, on <code>{repo.ref || "the default branch"}</code>.
        {" "}Airflow checks it for new commits every few minutes.
      </p>
      {errors && errors.length > 0 && (
        <div className="banner error" style={{ marginTop: 12 }} data-testid="import-errors">
          <strong>{errors.length === 1 ? "A file" : `${errors.length} files`} in the repo didn't load.</strong>
          {errors.map((e) => (
            <details key={e.file} style={{ marginTop: 6 }}>
              <summary className="mono small">{e.file}</summary>
              <pre className="small" style={{ whiteSpace: "pre-wrap", margin: "6px 0 0" }}>{e.error}</pre>
            </details>
          ))}
        </div>
      )}
    </div>
  );
}
export default function Pipelines() {
  const { name } = useProject();
  const [params, setParams] = useSearchParams();
  const { data, error, reload } = usePoll(() => getPipelines(name), [name], 5_000);
  const [starting, setStarting] = useState(false);
  const [startError, setStartError] = useState<string>();
  const [started, setStarted] = useState<string>();

  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  if (data.pipelines.length === 0) {
    return (
      <div className="page">
        {data.repo && <Repo repo={data.repo} errors={data.importErrors} />}
        <div className="empty">
          {data.repo
            ? <>No pipelines yet. Push a DAG to the repo, and it shows here once Airflow has loaded it.</>
            : <>No pipelines yet. Name a git repo for them in the project's settings, or tag an Airflow DAG <code>project:{name}</code>.</>}{" "}
          <Link to="/tools/pipelines">Open Airflow</Link>
        </div>
      </div>
    );
  }
  const p = data.pipelines.find((x) => x.id === params.get("pipeline")) ?? data.pipelines[0];
  const latest = p.runs[0];
  const run = async () => {
    setStarting(true);
    setStartError(undefined);
    try {
      setStarted((await startRun(name, p.id)).run);
      reload();
    } catch (e) {
      setStartError((e as Error).message);
    } finally {
      setStarting(false);
    }
  };

  // The newest run's tasks, on one time axis.
  const t0 = Math.min(...p.tasks.map((t) => when(t.started)?.getTime() ?? Infinity));
  const t1 = Math.max(...p.tasks.map((t) => (when(t.ended) ?? (when(t.started) ? new Date() : undefined))?.getTime() ?? -Infinity));
  const span = isFinite(t0) && isFinite(t1) && t1 > t0 ? t1 - t0 : 0;

  return (
    <div className="page">
      <div className="split">
        <nav className="side side-list" aria-label="Pipelines">
          <p className="eyebrow">Pipelines</p>
          {data.pipelines.map((x) => (
            <button key={x.id} className={x === p ? "active" : ""} onClick={() => setParams({ pipeline: x.id })} data-testid={`pipeline-${x.id}`}>
              <span className="mono">{x.id}</span><StateDot state={x.runs[0]?.state} />
            </button>
          ))}
        </nav>

        <section className="main stack">
          {data.repo && <Repo repo={data.repo} errors={data.importErrors} />}
          <div className="card">
            <div className="card-head" style={{ alignItems: "flex-start" }}>
              <div>
                <h2 className="mono" style={{ fontSize: 18 }}>{p.id}</h2>
                <p className="muted small" style={{ margin: "4px 0 0" }}>{p.description || "No description."}{p.paused && <> · <span className="pill">paused</span></>}</p>
                <p className="muted small" style={{ margin: "4px 0 0" }} data-testid="pipeline-source">
                  {p.bundle === "dags-folder" ? "The platform's own, in Airflow's DAGs folder." : <>From the repo, at <code title={p.commit}>{short(p.commit) ?? "–"}</code>.</>}
                </p>
              </div>
              <div className="row">
                <Link className="btn" to="/tools/pipelines">Edit in Airflow</Link>
                {data.canRun && <button className="btn primary" onClick={run} disabled={starting} data-testid="run-now">{starting ? "Starting…" : "Run now"}</button>}
              </div>
            </div>
            <ErrorBanner error={startError} />
            {p.clashesWith && p.clashesWith.length > 0 && (
              <div className="banner warn" data-testid="pipeline-clash">
                Another repo has a DAG with this ID too ({p.clashesWith.map((b) => b.replace(/^project-/, "")).join(", ")}). Airflow keeps only one at a time,
                so this one may show the other's code. Give one of them a new <code>dag_id</code>.
              </div>
            )}
            {started && <div className="banner info" data-testid="run-started">Started run <code>{started}</code>. It runs as the project's pipelines service account.</div>}
            {!data.canRun && <p className="small muted" style={{ margin: 0 }}>The project's editors can start runs.</p>}

            <p className="eyebrow" style={{ marginTop: 18 }}>Recent runs</p>
            <div className="runs" data-testid="runs">
              {[...p.runs].reverse().map((r) => (
                <button key={r.id} className={`${r.state}${r === latest ? " selected" : ""}`} title={`${r.state}, ${when(r.started)?.toLocaleString() ?? "not started"}`} aria-label={`${r.id}: ${r.state}`} />
              ))}
              {p.runs.length === 0 && <span className="small muted">No runs yet.</span>}
            </div>
          </div>

          {latest && (
            <div className="card">
              <div className="card-head">
                <h2>Latest run</h2>
                <span className="row small"><StateDot state={latest.state} /><strong>{latest.state}</strong>
                  <span className="muted">{ago(when(latest.started))}{took(when(latest.started), when(latest.ended)) && `, took ${took(when(latest.started), when(latest.ended))}`}{latest.requestedBy && `, started by ${latest.requestedBy}`}</span>
                </span>
              </div>
              <div className="gantt" data-testid="tasks">
                {p.tasks.map((t) => {
                  const a = when(t.started)?.getTime(), b = (when(t.ended) ?? (a ? new Date() : undefined))?.getTime();
                  return (
                    <div key={t.id} style={{ display: "contents" }}>
                      <span className="mono small">{t.id}</span>
                      <div className="track">
                        {a && b && span > 0 && <div className={`span ${t.state}`} style={{ left: `${((a - t0) / span) * 100}%`, width: `${((b - a) / span) * 100}%` }} />}
                      </div>
                      <span className="small muted">{t.state || "waiting"}</span>
                    </div>
                  );
                })}
              </div>
            </div>
          )}

          <div className="card flush">
            <table className="t">
              <thead><tr><th>Run</th><th>Commit</th><th>Started</th><th>Took</th><th>By</th></tr></thead>
              <tbody>
                {p.runs.map((r) => (
                  <tr key={r.id}>
                    <td><span className="row" style={{ gap: 8 }}><StateDot state={r.state} /><span className="mono small">{r.id}</span></span></td>
                    <td className="mono small" title={r.commit}>{short(r.commit) ?? "–"}</td>
                    <td className="small">{when(r.started)?.toLocaleString() ?? "–"}</td>
                    <td className="small muted">{took(when(r.started), when(r.ended)) || (r.state === "running" ? "running" : "–")}</td>
                    <td className="small muted">{r.requestedBy ?? (r.type === "scheduled" ? "schedule" : r.type)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </div>
  );
}
