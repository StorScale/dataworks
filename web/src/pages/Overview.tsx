import { Fragment, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { ago, deleteProject, Flow, getCopies, FlowTable, getFlow, getPipelines, getProjectAlerts, getSemantic, muteProjectAlert, ProjectIssue, took, when } from "../api";
import { can, checkName, ErrorBanner, failing, grants, StateDot, usePlatform, usePoll, useProject } from "../lib";

// levels puts each table in a column: sources first, then what's made from them.
export function levels(flow: Flow): FlowTable[][] {
  const level: Record<string, number> = {};
  for (const t of flow.tables) level[t.fqn] = 0;
  for (let pass = 0; pass < flow.tables.length; pass++) {
    for (const e of flow.edges) {
      if (level[e.from] !== undefined && level[e.to] !== undefined && level[e.to] <= level[e.from]) level[e.to] = level[e.from] + 1;
    }
  }
  const cols: FlowTable[][] = [];
  for (const t of flow.tables) (cols[level[t.fqn]] ??= []).push(t);
  return cols.filter(Boolean);
}

// FlowStrip is the project's tables in the order data flows through them.
export function FlowStrip({ flow, project, here }: { flow: Flow; project: string; here?: string }) {
  const navigate = useNavigate();
  const cols = levels(flow);
  const pipelineInto = (fqn: string) => flow.edges.find((e) => e.to === fqn)?.pipeline;
  return (
    <div className="flowstrip" data-testid="flow">
      {cols.map((col, i) => (
        <Fragment key={i}>
          {i > 0 && (
            <div className="arrow" aria-hidden title={pipelineInto(col[0].fqn) ? `made by ${pipelineInto(col[0].fqn)}` : undefined}>
              →{pipelineInto(col[0].fqn) && <div className="small mono" style={{ fontSize: 11 }}>{pipelineInto(col[0].fqn)}</div>}
            </div>
          )}
          <div className="stack" style={{ gap: 8 }}>
            {col.map((t) => (
              <button
                key={t.fqn}
                className={`node${failing(t.checks).length ? " alert" : ""}${t.name === here ? " here" : ""}`}
                style={{ textAlign: "left", font: "inherit", cursor: t.external ? "default" : "pointer", color: "var(--ink)" }}
                onClick={() => !t.external && navigate(`/p/${project}/data?table=${t.name}`)}
                data-testid={`node-${t.name}`}
              >
                <div className="kind">{t.external ? "from another project" : `${t.columns.length} columns`}</div>
                <div className="name">{t.external ? t.name.split(".").slice(-2).join(".") : t.name}</div>
                {failing(t.checks).length > 0 && <div className="small" style={{ color: "var(--bad-ink)" }}>{failing(t.checks).length} check failing</div>}
              </button>
            ))}
          </div>
        </Fragment>
      ))}
    </div>
  );
}

export default function Overview() {
  const { name, view, reload } = useProject();
  const { admin, flow: hasFlow, semantic, pipelines: hasPipelines } = usePlatform();
  const navigate = useNavigate();
  const spec = view?.spec?.spec;
  const flow = usePoll(() => (spec?.tables && hasFlow ? getFlow(name) : Promise.resolve(undefined)), [name, !!spec?.tables], 30_000);
  const model = usePoll(() => (spec?.tables && semantic ? getSemantic(name) : Promise.resolve(undefined)), [name, !!spec?.tables]);
  const runs = usePoll(() => (spec?.pipelines && hasPipelines ? getPipelines(name) : Promise.resolve(undefined)), [name, !!spec?.pipelines], 30_000);
  const [confirm, setConfirm] = useState(false);
  const [error, setError] = useState<string>();
  if (!view) return <div className="page"><div className="spinner" /></div>;

  const tables = flow.data?.tables.filter((t) => !t.external) ?? [];
  const checks = tables.flatMap((t) => t.checks.map((c) => ({ t, c })));
  const bad = checks.filter(({ c }) => c.status === "Failed");
  const pipeline = runs.data?.pipelines[0];
  const last = pipeline?.runs[0];
  const mine = view.role ?? (admin ? undefined : "");
  const remove = async () => {
    try {
      await deleteProject(name);
      navigate("/admin/projects");
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <div className="page">
      <ErrorBanner error={error} />
      {bad.length > 0 && (
        <div className="card alert" style={{ marginBottom: 20 }} data-testid="failing-checks">
          <div className="row" style={{ alignItems: "flex-start" }}>
            <span className="dot red" style={{ marginTop: 6 }} />
            <div className="grow">
              {bad.map(({ t, c }) => (
                <div key={t.fqn + c.name}>
                  <strong><span className="mono">{t.name}</span>: {checkName(name, t.name, c.name)} is failing.</strong>{" "}
                  <span className="muted">{c.result}</span>
                </div>
              ))}
            </div>
            <Link className="btn small" to={`data?table=${bad[0].t.name}&tab=quality`}>See the check</Link>
          </div>
        </div>
      )}

      {spec?.tables && (
        <div className="grid tiles" style={{ marginBottom: 20 }}>
          <Link to="data" className="card tile" style={{ color: "var(--ink)" }}>
            <div className="label">Tables</div><div className="value">{flow.data ? tables.length : "…"}</div>
            <div className="note">{namespaceNote(name, spec.tables.namespace)}</div>
          </Link>
          <Link to={bad.length ? `data?table=${bad[0].t.name}&tab=quality` : "data"} className={`card tile${bad.length ? " alert" : ""}`} style={{ color: "var(--ink)" }}>
            <div className="label">Checks</div><div className="value">{flow.data ? `${checks.length - bad.length}/${checks.length}` : "…"}</div>
            <div className="note">{bad.length ? `${bad.length} failing` : checks.length ? "all passing" : "none yet"}</div>
          </Link>
          {semantic && (
            <Link to="metrics" className="card tile" style={{ color: "var(--ink)" }}>
              <div className="label">Metrics</div><div className="value">{model.data ? model.data.metrics.length : "…"}</div>
              <div className="note">for people, dashboards and agents</div>
            </Link>
          )}
          {spec.pipelines && hasPipelines && (
            <Link to="pipelines" className="card tile" style={{ color: "var(--ink)" }}>
              <div className="label">Last pipeline run</div>
              <div className="value row" style={{ fontSize: 20, gap: 8 }}>{last ? <><StateDot state={last.state} />{last.state}</> : runs.data ? "none" : "…"}</div>
              <div className="note">{last ? `${pipeline!.id}, ${ago(when(last.started))}${took(when(last.started), when(last.ended)) ? `, took ${took(when(last.started), when(last.ended))}` : ""}` : ""}</div>
            </Link>
          )}
        </div>
      )}

      <div className="grid main-side">
        <div className="stack">
          {spec?.tables && (
            <section className="card">
              <div className="card-head"><h2>How data flows</h2><Link to="data" className="small">All tables</Link></div>
              <ErrorBanner error={flow.error} />
              {!flow.data ? <div className="spinner" /> : tables.length === 0 ? (
                <p className="muted">The catalog hasn't seen this project's tables yet. It looks every minute.</p>
              ) : <FlowStrip flow={flow.data} project={name} />}
            </section>
          )}
          {spec && (
            <section className="card" data-testid="grants">
              <h2>What you can do here</h2>
              <p className="muted small" style={{ margin: "4px 0 12px" }}>
                {mine === "editor" ? "You're an editor." : mine === "reader" ? "You're a reader." : mine ? `Your role here is ${mine}.` : "You administer the platform; you're not a member of this project."}{" "}
                Each role, as the platform applies it in Trino (through Ranger), Buckets and Keycloak:
              </p>
              <div className="grid cards" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))" }}>
                {grants(name, spec).map((g) => (
                  <div key={g.role} className="card" style={g.key === mine ? { borderColor: "var(--accent)", borderWidth: 2 } : { background: "var(--ground)" }}>
                    <div className="card-head" style={{ marginBottom: 6 }}>
                      <strong>{g.role}</strong>{g.key === mine && <span className="pill blue">you</span>}
                    </div>
                    <ul style={{ margin: 0, paddingLeft: 18 }} className="small">{g.items.map((i) => <li key={i}>{i}</li>)}</ul>
                  </div>
                ))}
              </div>
            </section>
          )}
        </div>

        <aside className="stack">
          <Alerts name={name} canMute={can(view, "mute-alerts")} />
          <section className="card">
            <div className="card-head"><h2>People</h2><Link to="access" className="small">Access</Link></div>
            {view.status?.members?.length ? (
              <table className="t" data-testid="members" style={{ margin: "0 -16px", width: "calc(100% + 32px)" }}>
                <tbody>
                  {view.status.members.map((m) => (
                    <tr key={m.username}>
                      <td>{m.username}</td>
                      <td><span className={`pill ${m.role === "editor" ? "blue" : ""}`}>{m.role}</span></td>
                      <td className="muted small">{m.role === "pipelines" ? "service account" : m.via ? `via ${m.via}` : "named"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : <p className="muted small">The operator hasn't applied the project yet.</p>}
          </section>
          <section className="card">
            <h2>Applied to</h2>
            <div className="list small" style={{ marginTop: 10 }}>
              {(view.status?.parts ?? []).map((p) => (
                <div key={p.system} className="row" style={{ alignItems: "flex-start" }}>
                  <span className={`dot ${p.ok ? "green" : "red"}`} style={{ marginTop: 5 }} />
                  <div className="grow"><strong>{p.system}</strong>{!p.ok && <div style={{ color: "var(--bad-ink)" }}>{p.message}</div>}</div>
                </div>
              ))}
              {view.status && <div className="muted">Checked {ago(new Date(view.status.updated))}.</div>}
            </div>
          </section>
          {view.spec?.spec.sources?.some((s) => s.copy) && <Copies project={name} />}
          {admin && view.spec && (
            <section className="card">
              <h2>Administration</h2>
              <div className="row" style={{ marginTop: 12 }}>
                <Link className="btn" to="edit" data-testid="edit-project">Edit project</Link>
                {confirm ? (
                  <>
                    <span className="small">Delete {name}? Its access and the credentials it keeps go; its data stays.</span>
                    <button className="btn danger" onClick={remove} data-testid="confirm-delete">Delete</button>
                    <button className="btn" onClick={() => setConfirm(false)}>Cancel</button>
                  </>
                ) : <button className="btn danger" onClick={() => setConfirm(true)} data-testid="delete-project">Delete</button>}
              </div>
              <button className="linkish small" style={{ marginTop: 10 }} onClick={reload}>Refresh status</button>
            </section>
          )}
        </aside>
      </div>
    </div>
  );
}

// Alerts is what's wrong in the project now (a pipeline's run failed, a check
// failing, a table stale), as the platform tells the project's channel of it.
function Alerts({ name, canMute }: { name: string; canMute: boolean }) {
  const { data, error, reload } = usePoll(() => getProjectAlerts(name), [name], 30_000);
  const [muteError, setMuteError] = useState<string>();
  if (!data?.checked) return null;
  const mute = async (i: ProjectIssue, hours: number) => {
    setMuteError(undefined);
    try { await muteProjectAlert(name, i.key, hours); reload(); } catch (e) { setMuteError((e as Error).message); }
  };
  const path = (link: string) => { try { const u = new URL(link); return u.pathname + u.search; } catch { return link; } };
  const n = data.notify;
  const where = [n?.teams && "its Teams channel", n?.slack && "its Slack channel", n?.email.length && n.email.join(", ")].filter(Boolean).join(" and ");
  return (
    <section className="card" data-testid="project-alerts">
      <h2>Alerts</h2>
      <ErrorBanner error={error ?? muteError} />
      {data.issues.length === 0 ? <p className="muted small" style={{ marginTop: 8 }}>Nothing wrong just now.</p> : (
        <div className="list small" style={{ marginTop: 10 }}>
          {data.issues.map((i) => (
            <div key={i.key} className="row" style={{ alignItems: "flex-start" }} data-testid={`issue-${i.key}`}>
              <span className={`dot ${i.kind === "stale" ? "amber" : "red"}`} style={{ marginTop: 5 }} />
              <div className="grow">
                <Link to={path(i.link)}><strong>{i.title}</strong></Link>
                {i.detail && <div className="muted">{i.detail}</div>}
                {i.mutedUntil && <div className="muted">Muted until {new Date(i.mutedUntil).toLocaleString()}</div>}
              </div>
              {canMute && (i.mutedUntil
                ? <button className="linkish small" onClick={() => mute(i, 0)}>Unmute</button>
                : <button className="linkish small" onClick={() => mute(i, 24)} title="No alerts about this for a day">Mute</button>)}
            </div>
          ))}
        </div>
      )}
      {(data.unknown ?? []).map((u) => <p key={u} className="small" style={{ marginTop: 8, color: "var(--warn-ink)" }}>Can't tell {u}</p>)}
      <p className="muted small" style={{ marginTop: 10 }}>
        {where ? `Told to ${where}.` : "Not told anywhere yet: the platform's administrators set where, when they edit the project."}
      </p>
    </section>
  );
}

const namespaceNote = (name: string, ns?: string) => `in iceberg.${ns ?? name.replace(/-/g, "_")}`;

// Copies are the project's sources' tables copied in by change capture, each
// with when its last change was committed: how far behind its source.
function Copies({ project }: { project: string }) {
  const { data } = usePoll(() => getCopies(project), [project], 60_000);
  return (
    <section className="card" data-testid="copies">
      <h2>Copies</h2>
      <div className="list small" style={{ marginTop: 10 }}>
        {(data?.copies ?? []).map((c) => (
          <div key={c.table} className="row">
            <span className="mono grow" title={`from ${c.source}: ${c.from}`}>{c.table}</span>
            <span className="muted">{c.lastChange ? `changed ${ago(new Date(c.lastChange))}` : c.error ? "unknown" : "not yet"}</span>
          </div>
        ))}
        {data && data.copies.length === 0 && <span className="muted">None yet.</span>}
      </div>
    </section>
  );
}
