import { useState } from "react";
import { approveModel, getModels, getScorings, getServings, putModelCard, type RegisteredModel, type Scoring } from "../api";
import { Empty, Intro } from "../components";
import { ErrorBanner, Spinner, usePlatform, usePoll, useProject } from "../lib";

// A project's models (use case 15.1): its registered models with their cards
// and versions, each version's approval (by an editor other than who
// registered it: the alias "approved", which pipelines and the API load),
// and its experiments. Trained and tracked from a notebook with
// buckets_lake.mlflow("<project>"), in the project's own MLflow workspace.

export default function Models() {
  const { name, view } = useProject();
  const { user } = usePlatform();
  const list = usePoll(() => getModels(name), [name], 30_000);
  const [error, setError] = useState<string>();
  if (list.error && !list.data) return <div className="page"><ErrorBanner error={list.error} /></div>;
  if (!list.data) return <div className="page"><Spinner /></div>;
  const d = list.data;
  if (!d.available) return <div className="page"><Empty>MLflow isn't set up on this install.</Empty></div>;
  const approve = async (model: string, version: string) => {
    setError(undefined);
    try { await approveModel(name, model, version); list.reload(); } catch (e) { setError((e as Error).message); }
  };
  return (
    <div className="page stack" data-testid="models">
      <Intro>
        The project's models: trained and tracked in its own MLflow workspace, registered, and approved before a pipeline or the API uses them,
        by an editor other than the one who registered the version.
      </Intro>
      <ErrorBanner error={error} />
      {d.models.length === 0 ? <Empty>No registered models yet. Train and register one from a notebook (below).</Empty> : d.models.map((m) => (
        <ModelCard key={m.name} project={name} model={m} canEdit={!!d.canApprove} me={user.username} onApprove={approve} reload={list.reload} />
      ))}
      <Scorings project={name} />
      <Servings project={name} />
      <section className="card flush">
        <div className="card-head"><h2>Experiments</h2></div>
        {d.experiments.length === 0 ? <Empty inline>None yet.</Empty> : (
          <table className="t" data-testid="experiments">
            <thead><tr><th>Experiment</th><th>Last run</th></tr></thead>
            <tbody>{d.experiments.map((e) => <tr key={e.id}><td className="mono">{e.name}</td><td className="small muted">{e.updated ? new Date(e.updated).toLocaleString() : ""}</td></tr>)}</tbody>
          </table>)}
      </section>
      <div className="card stack" style={{ gap: 8 }}>
        <h2>From a notebook</h2>
        <pre className="code">{`import buckets_lake
mlflow = buckets_lake.mlflow("${name}")      # the project's workspace, as you
mlflow.set_experiment("churn")
with mlflow.start_run():                          # stamped with you and the data's commit
    ...                                           # train; mlflow.log_metric("auc", auc)
    mlflow.sklearn.log_model(model, name="model", registered_model_name="churn")`}</pre>
        <span className="field-help">Elsewhere, MLFLOW_TRACKING_URI=<span className="mono">{d.trackingUri}</span> with MLFLOW_TRACKING_TOKEN, a Keycloak token.</span>
      </div>
      {view?.role === "reader" && <div className="small muted">As a reader you see the project's models and experiments; its editors train, register and approve them.</div>}
    </div>
  );
}

function ModelCard({ project, model, canEdit, me, onApprove, reload }: { project: string; model: RegisteredModel; canEdit: boolean; me: string;
  onApprove: (model: string, version: string) => void; reload: () => void }) {
  const [editing, setEditing] = useState(false);
  const [card, setCard] = useState(model.description ?? "");
  const [error, setError] = useState<string>();
  const save = async () => {
    setError(undefined);
    try { await putModelCard(project, model.name, card); setEditing(false); reload(); } catch (e) { setError((e as Error).message); }
  };
  return (
    <div className="card stack" style={{ gap: 10 }} data-testid={`model-${model.name}`}>
      <div className="card-head"><h2 className="mono">{model.name}
        {model.approved ? <span className="pill green">version {model.approved} approved</span> : <span className="pill amber">no version approved</span>}</h2>
        {canEdit && !editing && <button className="btn small" onClick={() => setEditing(true)}>{model.description ? "Edit its card" : "Write its card"}</button>}</div>
      <ErrorBanner error={error} />
      {editing ? <>
        <textarea value={card} onChange={(e) => setCard(e.target.value)} rows={8} style={{ width: "100%" }} aria-label="Its card"
          placeholder={"Purpose: who it's for and what it decides.\nData: the tables it was trained on.\nMetrics: how it was evaluated.\nLimits: where it shouldn't be used."} />
        <div className="form-actions"><button className="btn primary" onClick={save}>Save the card</button><button className="btn" onClick={() => setEditing(false)}>Cancel</button></div>
      </> : model.description ? <div className="small" style={{ whiteSpace: "pre-wrap" }}>{model.description}</div> : <div className="small muted">No card yet: its purpose, data, metrics and limits.</div>}
      <div className="scroll-x"><table className="t">
        <thead><tr><th>Version</th><th>Registered</th><th>By</th><th>Approved</th><th></th></tr></thead>
        <tbody>{model.versions.map((v) => (
          <tr key={v.version}>
            <td className="mono">{v.version}</td>
            <td className="small">{v.created ? new Date(v.created).toLocaleString() : ""}</td>
            <td>{v.registeredBy ?? <span className="muted">–</span>}</td>
            <td className="small">{v.approvedBy ? <>by {v.approvedBy}{v.approvedAt ? `, ${new Date(v.approvedAt).toLocaleDateString()}` : ""}</> : <span className="muted">not yet</span>}</td>
            <td>{canEdit && !v.approvedBy && (v.registeredBy === me
              ? <span className="small muted">another editor approves it</span>
              : <button className="btn small primary" onClick={() => onApprove(model.name, v.version)} data-testid={`approve-${model.name}-${v.version}`}>Approve</button>)}</td>
          </tr>))}</tbody>
      </table></div>
    </div>
  );
}

// Scorings (R15.3): the project's approved models scoring its tables on a schedule (its spec's scoring), each
// run's rows, its scores' mean, and its inputs' drift (PSI against the version's first run).
function Scorings({ project }: { project: string }) {
  const list = usePoll(() => getScorings(project), [project], 60_000);
  const scorings = list.data?.scoring ?? [];
  return (
    <section className="card flush" data-testid="scorings">
      <div className="card-head"><h2>Scoring</h2></div>
      <p className="small muted card-note">
        A model's approved version scores a table on a schedule, as the project's pipelines, into a table of its own. Each run's inputs are compared
        with the version's first run's: a column whose population stability index passes the threshold is an alert.
      </p>
      {list.error && <div style={{ padding: "0 20px 12px" }}><ErrorBanner error={list.error} /></div>}
      {scorings.length === 0 ? <Empty inline>None yet: the project's spec names them (scoring).</Empty> : (
        <div className="scroll-x"><table className="t">
          <thead><tr><th>Scoring</th><th>Model</th><th>From, into</th><th>When</th><th>Last run</th><th>Drift</th></tr></thead>
          <tbody>{scorings.map((sc) => <ScoringRow key={sc.name} sc={sc} />)}</tbody>
        </table></div>
      )}
    </section>
  );
}

function ScoringRow({ sc }: { sc: Scoring }) {
  const last = sc.runs[0];
  const worst = last ? Object.entries(last.drift).sort((a, b) => b[1] - a[1])[0] : undefined;
  const past = !!worst && worst[1] > sc.threshold;
  return (
    <tr data-testid={`scoring-${sc.name}`}>
      <td className="mono">{sc.name}<div className="small muted">{sc.dag}</div></td>
      <td className="mono small">{sc.model}{last && <span className="muted"> v{last.version}</span>}</td>
      <td className="mono small">{sc.table} → {sc.output}</td>
      <td className="mono small">{sc.schedule}</td>
      <td className="small">{last ? <>{new Date(last.at).toLocaleString()}<div className="muted">{last.rows} rows{last.scoreMean !== undefined ? `, mean ${last.scoreMean.toFixed(3)}` : ""}</div></> : <span className="muted">not run yet</span>}</td>
      <td className="small">{worst
        ? <><span className={`pill ${past ? "red" : "green"}`}>{worst[1].toFixed(2)}</span> <span className="mono">{worst[0]}</span><div className="muted">past {sc.threshold.toFixed(2)} is drift</div></>
        : <span className="muted">–</span>}</td>
    </tr>
  );
}

// Servings (R15.4): the project's approved models answering as an API, through the platform: signed in, the project's
// people and agents only, each call in the audit log.
function Servings({ project }: { project: string }) {
  const list = usePoll(() => getServings(project), [project], 30_000);
  const servings = list.data?.serving ?? [];
  const first = servings[0];
  return (
    <section className="card flush" data-testid="servings">
      <div className="card-head"><h2>Serving</h2></div>
      <p className="small muted card-note">
        A model's approved version answers as an API, from a server of the project's that only the platform reaches. A caller signs in
        (a Keycloak token, as the API reference says) and must be one of the project's people or agents; each call is recorded in the audit log,
        with the version that answered. A newly approved version answers within a minute.
      </p>
      {list.error && <div style={{ padding: "0 20px 12px" }}><ErrorBanner error={list.error} /></div>}
      {servings.length === 0 ? <Empty inline>None yet: the project's spec names them (serving).</Empty> : <>
        <div className="scroll-x"><table className="t">
          <thead><tr><th>Serving</th><th>Model</th><th>Answering with</th><th>Rows a call</th><th>Call it at</th></tr></thead>
          <tbody>{servings.map((sv) => (
            <tr key={sv.name} data-testid={`serving-${sv.name}`}>
              <td className="mono">{sv.name}</td>
              <td className="mono small">{sv.model}</td>
              <td className="small">{sv.version ? <span className="pill green">version {sv.version}</span> : <span className="pill amber">{sv.error || "starting"}</span>}</td>
              <td className="small">{sv.maxRows}</td>
              <td className="mono small" style={{ overflowWrap: "anywhere" }}>POST {sv.url}</td>
            </tr>))}</tbody>
        </table></div>
        {first && <div className="card-foot stack" style={{ gap: 6 }}>
          <span className="field-label">From a script</span>
          <pre className="code">{`curl -s ${first.url} \\
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \\
  -d '{"dataframe_records": [{"<column>": <value>, ...}]}'
# {"predictions": [...], "model": "${first.model}", "version": "..."}`}</pre>
        </div>}
      </>}
    </section>
  );
}
