import { Link } from "react-router-dom";
import { ago, Flow, getFlow, getPipelines, getSemantic, listProjects, Pipeline, ProjectView, when } from "../api";
import { Page } from "../components";
import { HealthNotice } from "./Health";
import { checkName, failing, lastProject, Phase, RolePill, Spinner, StateDot, usePlatform, usePoll } from "../lib";

type Summary = { view: ProjectView; flow?: Flow; metrics?: number; pipelines?: Pipeline[] };

// Home: your projects, what needs attention in them, and what ran lately.
export default function Home() {
  const { user, admin, projects, flow, semantic, pipelines, tools } = usePlatform();
  const first = (user.name || user.username).split(" ")[0];
  const hour = new Date().getHours();
  const greeting = hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening";

  const { data, error } = usePoll<Summary[]>(async () => {
    if (!projects) return [];
    const { projects: all } = await listProjects();
    const mine = all.filter((p) => p.role || admin);
    return Promise.all(mine.map(async (view) => {
      const tables = !!view.spec?.spec.tables, piped = !!view.spec?.spec.pipelines;
      const [f, s, ps] = await Promise.all([
        tables && flow ? getFlow(view.name).catch(() => undefined) : undefined,
        tables && semantic ? getSemantic(view.name).catch(() => undefined) : undefined,
        piped && pipelines ? getPipelines(view.name).catch(() => undefined) : undefined,
      ]);
      return { view, flow: f, metrics: s?.metrics.length, pipelines: ps?.pipelines };
    }));
  }, [projects, admin], 30_000);

  const attention = (data ?? []).flatMap(({ view, flow, pipelines }) => [
    ...(flow?.tables ?? []).filter((t) => !t.external).flatMap((t) => failing(t.checks).map((c) => ({
      key: `${view.name}/${t.name}/${c.name}`, project: view.name, to: `/p/${view.name}/data?table=${t.name}&tab=quality`,
      title: <><span className="mono">{t.name}</span>: {checkName(view.name, t.name, c.name)} is failing</>, detail: c.result,
    }))),
    ...(pipelines ?? []).filter((p) => p.runs[0]?.state === "failed").map((p) => ({
      key: `${view.name}/${p.id}`, project: view.name, to: `/p/${view.name}/pipelines?pipeline=${p.id}`,
      title: <><span className="mono">{p.id}</span>'s last run failed</>, detail: ago(when(p.runs[0].started)),
    })),
  ]);
  const runs = (data ?? []).flatMap(({ view, pipelines }) => (pipelines ?? []).flatMap((p) => p.runs.slice(0, 3).map((r) => ({ project: view.name, pipeline: p.id, run: r }))))
    .sort((a, b) => (when(b.run.started)?.getTime() ?? 0) - (when(a.run.started)?.getTime() ?? 0)).slice(0, 6);
  const last = lastProject();
  const embedded = tools.filter((t) => t.section === "work");

  return (
    <Page>
      <div className="page-head">
        <div>
          <h1 data-testid="greeting">{greeting}, {first}</h1>
          <p className="lead">Your projects: their data, metrics and pipelines, with one sign-in. What you can see and change follows your role in each.</p>
        </div>
        {last && data?.some((d) => d.view.name === last) && <Link className="btn primary" to={`/p/${last}`}>Continue in {last}</Link>}
      </div>
      {error && <div className="banner error">{error}</div>}
      <HealthNotice />

      <div className="grid main-side">
        <div className="stack">
          {projects && (
            <section>
              <p className="eyebrow">Your projects</p>
              {!data ? <Spinner /> : data.length === 0 ? (
                <div className="empty" data-testid="my-projects">You're not in any project yet. Ask an administrator to add you.</div>
              ) : (
                <div className="grid cards" data-testid="my-projects">
                  {data.map(({ view, flow, metrics, pipelines }) => {
                    const tables = flow?.tables.filter((t) => !t.external) ?? [];
                    const bad = tables.reduce((n, t) => n + failing(t.checks).length, 0);
                    const run = pipelines?.[0]?.runs[0];
                    return (
                      <Link key={view.name} to={`/p/${view.name}`} className="card" data-testid={`project-${view.name}`}>
                        <div className="card-head" style={{ marginBottom: 6 }}>
                          <h2>{view.name}</h2>
                          <RolePill role={view.role} admin={admin} />
                        </div>
                        <p className="muted small" style={{ margin: "0 0 12px" }}>{view.spec?.spec.description || "No description."}</p>
                        <div className="row small">
                          {view.status?.phase !== "Ready" && <Phase v={view} />}
                          {flow && <span>{tables.length} tables</span>}
                          {metrics !== undefined && <span>{metrics} metrics</span>}
                          {bad > 0 ? <span className="pill red">{bad} failing</span> : flow && tables.some((t) => t.checks.length) && <span className="pill green">checks passing</span>}
                          {run && <span className="row" style={{ gap: 6 }}><StateDot state={run.state} /> ran {ago(when(run.started))}</span>}
                        </div>
                      </Link>
                    );
                  })}
                </div>
              )}
            </section>
          )}

          <section className="card flush">
            <div className="card-head" style={{ padding: "16px 20px 0" }}><h2>Recent pipeline runs</h2></div>
            {!data ? <div style={{ padding: 20 }}><Spinner /></div> : runs.length === 0 ? (
              <p className="muted" style={{ padding: "0 20px 16px" }}>No runs in your projects yet.</p>
            ) : (
              <table className="t" data-testid="recent-runs">
                <thead><tr><th>Pipeline</th><th>Project</th><th>Started</th><th>By</th></tr></thead>
                <tbody>
                  {runs.map(({ project, pipeline, run }) => (
                    <tr key={project + run.id}>
                      <td><span className="row" style={{ gap: 8 }}><StateDot state={run.state} /><Link className="mono" to={`/p/${project}/pipelines?pipeline=${pipeline}`}>{pipeline}</Link></span></td>
                      <td>{project}</td>
                      <td className="muted">{ago(when(run.started))}</td>
                      <td className="muted">{run.requestedBy ?? (run.type === "scheduled" ? "schedule" : run.type)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </section>
        </div>

        <aside className="stack">
          <section className={`card${attention.length ? " alert" : ""}`} data-testid="attention">
            <div className="card-head"><h2>Needs attention</h2>{attention.length > 0 && <span className="pill red">{attention.length}</span>}</div>
            {!data ? <Spinner /> : attention.length === 0 ? <p className="muted small">Nothing: every check is passing and every last run succeeded.</p> : (
              <div className="list">
                {attention.map((a) => (
                  <Link key={a.key} to={a.to} style={{ display: "block", color: "var(--ink)" }}>
                    <div className="row" style={{ gap: 8 }}><span className="dot red" /><strong className="small">{a.title}</strong></div>
                    <div className="small muted" style={{ marginLeft: 17 }}>{a.project} · {a.detail}</div>
                  </Link>
                ))}
              </div>
            )}
          </section>
          <section className="card">
            <h2>Also on the platform</h2>
            <div className="list small">
              {embedded.map((t) => t.embedUrl ? (
                <Link key={t.id} to={`/tools/${t.id}`} style={{ display: "block" }} data-testid={`card-${t.id}`}>
                  <strong>{t.name}</strong><div className="muted">{t.description}</div>
                </Link>
              ) : (
                <a key={t.id} href={t.url} target="_blank" rel="noreferrer" style={{ display: "block" }} data-testid={`card-${t.id}`}>
                  <strong>{t.name} ↗</strong><div className="muted">{t.description}</div>
                </a>
              ))}
              <div className="muted">Trino, for JDBC and CLI clients, is at <code>https://localhost:8443</code>: sign in with a Keycloak token.</div>
            </div>
          </section>
        </aside>
      </div>
    </Page>
  );
}
