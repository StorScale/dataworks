import { Link } from "react-router-dom";
import { useState } from "react";
import { approveAgent, getAgents, getLLM, putAgent, removeAgent, type LLMUse } from "../api";
import { ErrorBanner, Spinner, usePlatform, usePoll, useProject } from "../lib";

// Agents are programs of the project's own (use case 12.2): each with an
// identity of its own, the tools it may use, an owner, approved by an editor
// other than the one who registered it, and revoked at once.
export default function Agents() {
  const { name, view } = useProject();
  const { admin, user } = usePlatform();
  const list = usePoll(() => getAgents(name), [name], 15_000);
  const llm = usePoll(() => getLLM(name), [name], 30_000);
  const useOf = (who: string): LLMUse | undefined => llm.data?.uses.find((u) => u.who === who);
  const [error, setError] = useState<string>();
  const [adding, setAdding] = useState(false);
  const [secret, setSecret] = useState<{ agent: string; clientId: string; secret: string; mcpUrl: string }>();
  const canEdit = view?.role === "editor" || admin;
  if (list.error && !list.data) return <div className="page"><ErrorBanner error={list.error} /></div>;
  if (!list.data) return <div className="page"><Spinner /></div>;
  const act = async (f: () => Promise<unknown>) => { setError(undefined); try { await f(); list.reload(); } catch (e) { setError((e as Error).message); } };
  return (
    <div className="page stack" data-testid="agents">
      <p className="lead" style={{ margin: 0 }}>
        Programs of the project's own that read its data through the MCP server, each with an identity of its own and only the tools it was given.
        An agent is approved by an editor other than the one who registered it, and revoked at once.
      </p>
      <ErrorBanner error={error} />
      {secret && (
        <div className="banner info" data-testid="agent-secret">
          <strong>{secret.agent} is approved.</strong> Its client is <span className="mono">{secret.clientId}</span>, its secret <span className="mono">{secret.secret}</span>
          {" "}(shown once: give it to the agent now), and it talks to <span className="mono">{secret.mcpUrl}</span> with a token from Keycloak (client credentials).
          <div><button className="btn small" onClick={() => setSecret(undefined)}>Done</button></div>
        </div>
      )}
      {llm.data?.gateway && (
        <div className="card stack" style={{ gap: 4 }} data-testid="llm">
          <strong>The LLM gateway</strong>
          <div className="small muted">Models are reached through the platform's gateway at <span className="mono">{llm.data.gateway}</span>, with a key per project and per agent, each with a monthly budget (USD). A key at its budget gets no more answers this month, and the project is alerted.</div>
          {llm.data.uses.length === 0 && <div className="small muted">No budget set: give the project one (its settings, <span className="mono">llm.budget</span>) or an agent one, and the operator makes the key, kept as a connection the pipelines and notebooks read.</div>}
          {llm.data.uses.map((u) => (
            <div key={u.who} className="row small" data-testid={`llm-${u.who}`}>
              <span className="mono">{u.who === name ? "the project" : u.who}</span>
              <span>{u.key ? `${u.spend.toFixed(2)} of ${u.budget.toFixed(2)} spent` : "no key yet"}</span>
              {u.share !== undefined && <span className={"pill " + (u.share >= 1 ? "red" : u.share >= 0.8 ? "amber" : "green")}>{Math.round(u.share * 100)}%</span>}
              {u.error && <span className="muted">{u.error}</span>}
            </div>
          ))}
        </div>
      )}
      {canEdit && !adding && <div className="row"><button className="btn primary" onClick={() => setAdding(true)} data-testid="add-agent">Register an agent</button></div>}
      {adding && <AgentForm project={name} tools={list.data.tools} taken={list.data.agents.map((a) => a.name)} owner={user.username} onDone={() => { setAdding(false); list.reload(); }} />}
      {list.data.agents.length === 0 && <p className="muted">No agents yet.</p>}
      {list.data.agents.map((a) => (
        <div key={a.name} className="card stack" style={{ gap: 6 }} data-testid={`agent-${a.name}`}>
          <div className="row">
            <h3 style={{ margin: 0 }} className="mono">{a.name}</h3>
            <span className={"pill " + (a.approved ? "green" : "amber")}>{a.approved ? `approved by ${a.approvedBy}` : "awaiting approval"}</span>
            <span className="grow" />
            {canEdit && !a.approved && a.registeredBy !== user.username && (
              <button className="btn small primary" data-testid={`approve-${a.name}`}
                onClick={() => act(async () => { const r = await approveAgent(name, a.name); if (r.secret) setSecret({ agent: a.name, clientId: r.clientId, secret: r.secret, mcpUrl: r.mcpUrl }); })}>Approve</button>
            )}
            {canEdit && <button className="btn small" onClick={() => { if (confirm(`Revoke ${a.name}? It signs in no more.`)) act(() => removeAgent(name, a.name)); }} data-testid={`revoke-${a.name}`}>Revoke</button>}
          </div>
          <div className="small">
            {a.description && <>{a.description} </>}
            Owner <span className="mono">{a.owner}</span>; client <span className="mono">{a.clientId}</span>; tools: <span className="mono">{(a.tools ?? []).join(", ") || "every tool a reader may use"}</span>
            {a.schedule && <>; runs <span className="mono">{a.notebook}</span> at <span className="mono">{a.schedule}</span> (UTC){a.dag ? <> as the pipeline <Link to={`/projects/${encodeURIComponent(name)}/pipelines`} className="mono">{a.dag}</Link></> : ", once approved"}</>}
            {a.budget ? <>; {useOf(a.name)?.key ? `${useOf(a.name)!.spend.toFixed(2)} of` : "a budget of"} {a.budget} USD a month at the gateway</> : null}.
            {a.registeredBy && <> Registered by {a.registeredBy}.</>}
          </div>
        </div>
      ))}
    </div>
  );
}

function AgentForm({ project, tools, taken, owner, onDone }: { project: string; tools: string[]; taken: string[]; owner: string; onDone: () => void }) {
  const [agentName, setName] = useState("");
  const [description, setDescription] = useState("");
  const [picked, setPicked] = useState<string[]>(["list_metrics", "query_metrics"]);
  const [schedule, setSchedule] = useState("");
  const [notebook, setNotebook] = useState("");
  const [budget, setBudget] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const ok = /^[a-z][a-z0-9-]{0,30}$/.test(agentName) && !taken.includes(agentName);
  const save = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await putAgent(project, { name: agentName, description: description || undefined, owner, tools: picked, schedule: schedule || undefined, notebook: notebook || undefined, budget: budget ? Number(budget) : undefined });
      onDone();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <form className="card stack" style={{ gap: 10 }} onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="agent-form">
      <strong>A new agent</strong>
      <ErrorBanner error={error} />
      <div className="row">
        <label>Name <input className="mono" value={agentName} onChange={(e) => setName(e.target.value)} placeholder="nightly-summary" data-testid="agent-name" /></label>
        <label>What it's for <input value={description} onChange={(e) => setDescription(e.target.value)} style={{ width: 320 }} /></label>
        <label>Schedule <input className="mono" value={schedule} onChange={(e) => setSchedule(e.target.value)} placeholder="0 6 * * * (UTC), or none" data-testid="agent-schedule" /></label>
        <label>Notebook <input className="mono" value={notebook} onChange={(e) => setNotebook(e.target.value)} placeholder="notebooks/nightly.ipynb, in the repo" style={{ width: 240 }} data-testid="agent-notebook" /></label>
        <label>LLM budget (USD a month) <input className="mono" value={budget} onChange={(e) => setBudget(e.target.value)} placeholder="none" style={{ width: 90 }} data-testid="agent-budget" /></label>
      </div>
      <div className="small muted">Tools it may use (of the MCP server's); none picked means every tool a reader may use. With a schedule and a notebook (an .ipynb in the project's repo, under the pipelines' path), the platform runs the notebook as the agent: <span className="mono">buckets_lake.agent()</span> is the MCP server, <span className="mono">buckets_lake.ask()</span> the gateway, on its budget.</div>
      <div className="chips">
        {tools.map((t) => <button key={t} type="button" className={`chip${picked.includes(t) ? " on" : ""}`} aria-pressed={picked.includes(t)}
          onClick={() => setPicked(picked.includes(t) ? picked.filter((x) => x !== t) : [...picked, t])} data-testid={`tool-${t}`}>{t}</button>)}
      </div>
      <div className="row">
        <button className="btn primary" type="submit" disabled={!ok || busy} data-testid="register-agent">{busy ? "Saving…" : "Register"}</button>
        <button className="btn" type="button" onClick={onDone}>Cancel</button>
        <span className="small muted">Owner: {owner}. Another editor approves it before it may sign in.</span>
      </div>
    </form>
  );
}
