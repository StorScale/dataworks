import { useState } from "react";
import { applyPlan, getPlans, makePlan, type Plan } from "../api";
import { ErrorBanner, Spinner, usePlatform, usePoll, useProject } from "../lib";
import { Empty, Intro } from "../components";

const pill: Record<Plan["state"], string> = { fetching: "", ready: "amber", failed: "red", applied: "green" };

// The project as code: its spec and metrics from its repo's storscale/ folder
// at a ref (a tag, a branch, a commit), planned, then applied by someone
// else. Rolling back is planning an earlier release.
export default function Releases() {
  const { name, reload: reloadProject } = useProject();
  const { user } = usePlatform();
  const { data, error, reload } = usePoll(() => getPlans(name), [name], 5_000);
  const [ref, setRef] = useState("");
  const [busy, setBusy] = useState<string>();
  const [actionError, setActionError] = useState<string>();
  const act = async (key: string, f: () => Promise<unknown>) => {
    setBusy(key);
    setActionError(undefined);
    try { await f(); reload(); } catch (e) { setActionError((e as Error).message); } finally { setBusy(undefined); }
  };
  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  const r = data.release;
  return (
    <div className="page stack" data-testid="releases">
      <Intro>
        The project's spec and metrics, from its repo's <span className="mono">storscale/</span> folder: <span className="mono">project.yaml</span>,{" "}
        <span className="mono">semantic.yaml</span>, and <span className="mono">targets/{data.target}.yaml</span> for this install ({data.target}).
        Plan a tag, a branch or a commit; someone other than who planned it applies it. To roll back, plan an earlier release: the tables keep the data written since (their history reads them as they were).
      </Intro>
      <div className="card small" data-testid="release">
        {r ? <>Released: <span className="mono">{r.ref}</span> ({r.commit?.slice(0, 12)}), planned by {r.by}, applied by {r.appliedBy}{" "}
          {r.appliedAt && new Date(r.appliedAt).toLocaleString()}.</> : <span className="muted">No release applied yet: the spec is as administrators last saved it.</span>}
      </div>
      {data.canPlan && (
        <form className="fields" onSubmit={(e) => { e.preventDefault(); if (ref.trim()) act("plan", () => makePlan(name, ref.trim()).then(() => setRef(""))); }}>
          <label>A release to plan<input className="mono" placeholder="v1.3, main, or a commit" value={ref} onChange={(e) => setRef(e.target.value)} data-testid="plan-ref" /></label>
          <button className="btn primary" disabled={!ref.trim() || busy === "plan"} data-testid="plan">{busy === "plan" ? "Planning…" : "Plan"}</button>
        </form>
      )}
      <ErrorBanner error={actionError} />
      {data.plans.length === 0 && <Empty>No plans yet.</Empty>}
      {data.plans.map((p) => (
        <div key={p.id} className="card stack" style={{ gap: 8 }} data-testid={`plan-${p.ref}`}>
          <div className="card-head">
            <h2 className="mono">{p.ref}{p.commit && <span className="mono small muted" style={{ fontWeight: 400 }}>{p.commit.slice(0, 12)}</span>}
              <span className={`pill ${pill[p.state]}`}>{p.state === "fetching" ? "checking out…" : p.state}</span></h2>
            {p.state === "ready" && data.canApply && p.by !== user?.username && (
              <button className="btn small primary" disabled={busy === p.id} data-testid={`apply-${p.ref}`}
                onClick={() => act(p.id, () => applyPlan(name, p.id).then(() => reloadProject()))}>
                {busy === p.id ? "Applying…" : "Apply"}
              </button>
            )}
          </div>
          <div className="small muted">
            Planned by {p.by} {new Date(p.at).toLocaleString()} for {p.target}
            {p.appliedBy && <>; applied by {p.appliedBy} {p.appliedAt && new Date(p.appliedAt).toLocaleString()}</>}.
            {p.state === "ready" && p.by === user?.username && " Someone else applies it."}
            {p.state === "ready" && !data.canApply && " An administrator applies it."}
          </div>
          {p.error && <div className="small" style={{ color: "var(--bad-ink)" }}>{p.error}</div>}
          {(p.state === "ready" || p.state === "applied") && (
            p.changes.length === 0 ? <div className="small muted">No changes.</div> :
              <ul className="small" style={{ margin: 0 }}>{p.changes.map((c) => <li key={c}>{c}</li>)}</ul>
          )}
        </div>
      ))}
    </div>
  );
}
