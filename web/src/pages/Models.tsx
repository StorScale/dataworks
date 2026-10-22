import { useState } from "react";
import { approveModel, getModels, putModelCard, type RegisteredModel } from "../api";
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
  if (!d.available) return <div className="page"><div className="empty">MLflow isn't set up on this install.</div></div>;
  const approve = async (model: string, version: string) => {
    setError(undefined);
    try { await approveModel(name, model, version); list.reload(); } catch (e) { setError((e as Error).message); }
  };
  return (
    <div className="page stack" data-testid="models">
      <p className="lead" style={{ margin: 0 }}>
        The project's models: trained and tracked in its own MLflow workspace, registered, and approved before a pipeline or the API uses them,
        by an editor other than the one who registered the version.
      </p>
      <div className="card stack" style={{ gap: 4 }}>
        <strong>From a notebook</strong>
        <pre className="mono small" style={{ margin: 0, whiteSpace: "pre-wrap" }}>{`import buckets_lake
mlflow = buckets_lake.mlflow("${name}")      # the project's workspace, as you
mlflow.set_experiment("churn")
with mlflow.start_run():                          # stamped with you and the data's commit
    ...                                           # train; mlflow.log_metric("auc", auc)
    mlflow.sklearn.log_model(model, name="model", registered_model_name="churn")`}</pre>
        <span className="small muted">Elsewhere, MLFLOW_TRACKING_URI=<span className="mono">{d.trackingUri}</span> with MLFLOW_TRACKING_TOKEN, a Keycloak token.</span>
      </div>
      <ErrorBanner error={error} />
      {d.models.length === 0 ? <div className="empty">No registered models yet.</div> : d.models.map((m) => (
        <ModelCard key={m.name} project={name} model={m} canEdit={!!d.canApprove} me={user.username} onApprove={approve} reload={list.reload} />
      ))}
      <div className="card stack" style={{ gap: 4 }}>
        <strong>Experiments</strong>
        {d.experiments.length === 0 ? <span className="small muted">None yet.</span> : (
          <table className="t" data-testid="experiments">
            <thead><tr><th>Experiment</th><th>Last run</th></tr></thead>
            <tbody>{d.experiments.map((e) => <tr key={e.id}><td className="mono">{e.name}</td><td className="small muted">{e.updated ? new Date(e.updated).toLocaleString() : ""}</td></tr>)}</tbody>
          </table>)}
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
    <div className="card stack" style={{ gap: 6 }} data-testid={`model-${model.name}`}>
      <div className="row"><strong className="mono">{model.name}</strong>
        {model.approved ? <span className="pill">version {model.approved} approved</span> : <span className="pill amber">no version approved</span>}
        <span className="grow" />{canEdit && !editing && <button className="btn small" onClick={() => setEditing(true)}>{model.description ? "Edit its card" : "Write its card"}</button>}</div>
      <ErrorBanner error={error} />
      {editing ? <>
        <textarea value={card} onChange={(e) => setCard(e.target.value)} rows={8} style={{ width: "100%", boxSizing: "border-box" }}
          placeholder={"Purpose: who it's for and what it decides.\nData: the tables it was trained on.\nMetrics: how it was evaluated.\nLimits: where it shouldn't be used."} />
        <div className="row"><button className="btn primary small" onClick={save}>Save the card</button><button className="btn small" onClick={() => setEditing(false)}>Cancel</button></div>
      </> : model.description ? <div className="small" style={{ whiteSpace: "pre-wrap" }}>{model.description}</div> : <div className="small muted">No card yet: its purpose, data, metrics and limits.</div>}
      <table className="t">
        <thead><tr><th>Version</th><th>Registered</th><th>By</th><th>Approved</th><th></th></tr></thead>
        <tbody>{model.versions.map((v) => (
          <tr key={v.version}>
            <td className="mono">{v.version}</td>
            <td className="small">{v.created ? new Date(v.created).toLocaleString() : ""}</td>
            <td>{v.registeredBy ?? <span className="muted">–</span>}</td>
            <td className="small">{v.approvedBy ? <>by {v.approvedBy}{v.approvedAt ? `, ${new Date(v.approvedAt).toLocaleDateString()}` : ""}</> : <span className="muted">not yet</span>}</td>
            <td>{canEdit && !v.approvedBy && (v.registeredBy === me
              ? <span className="small muted">another editor approves it</span>
              : <button className="btn small" onClick={() => onApprove(model.name, v.version)} data-testid={`approve-${model.name}-${v.version}`}>Approve</button>)}</td>
          </tr>))}</tbody>
      </table>
    </div>
  );
}
