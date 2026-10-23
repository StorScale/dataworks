import { Link } from "react-router-dom";
import { useState } from "react";
import { approveAgent, buildKnowledge, deleteKnowledge, getAgents, getKnowledge, getLLM, getLLMCalls, putAgent, putKnowledge, removeAgent, searchKnowledge, type KnowledgeHit, type LLMUse } from "../api";
import { Empty, Intro } from "../components";
import { can, ErrorBanner, Spinner, usePlatform, usePoll, useProject } from "../lib";

// Agents are programs of the project's own (use case 12.2): each with an
// identity of its own, the tools it may use, an owner, approved by an editor
// other than the one who registered it, and revoked at once.
export default function Agents() {
  const { name, view } = useProject();
  const { admin, user } = usePlatform();
  const list = usePoll(() => getAgents(name), [name], 15_000);
  const llm = usePoll(() => getLLM(name), [name], 30_000);
  const useOf = (who: string): LLMUse | undefined => llm.data?.uses.find((u) => u.who === who);
  const knowledge = usePoll(() => getKnowledge(name), [name], 60_000);
  const [error, setError] = useState<string>();
  const [adding, setAdding] = useState(false);
  const [secret, setSecret] = useState<{ agent: string; clientId: string; secret: string; mcpUrl: string }>();
  const canEdit = can(view, "build"), canApprove = can(view, "approve");
  if (list.error && !list.data) return <div className="page"><ErrorBanner error={list.error} /></div>;
  if (!list.data) return <div className="page"><Spinner /></div>;
  const act = async (f: () => Promise<unknown>) => { setError(undefined); try { await f(); list.reload(); } catch (e) { setError((e as Error).message); } };
  return (
    <div className="page stack" data-testid="agents">
      <Intro actions={canEdit && !adding && <button className="btn primary" onClick={() => setAdding(true)} data-testid="add-agent">Register an agent</button>}>
        Programs of the project's own that read its data through the MCP server, each with an identity of its own and only the tools it was given.
        An agent is approved by an editor other than the one who registered it, and revoked at once.
      </Intro>
      <ErrorBanner error={error} />
      {secret && (
        <div className="banner info" data-testid="agent-secret">
          <strong>{secret.agent} is approved.</strong> Its client is <span className="mono">{secret.clientId}</span>, its secret <span className="mono">{secret.secret}</span>
          {" "}(shown once: give it to the agent now), and it talks to <span className="mono">{secret.mcpUrl}</span> with a token from Keycloak (client credentials).
          <div><button className="btn small" onClick={() => setSecret(undefined)}>Done</button></div>
        </div>
      )}
      {adding && <AgentForm project={name} tools={[...list.data.tools, ...(list.data.grants ?? [])]} taken={list.data.agents.map((a) => a.name)} owner={user.username} onDone={() => { setAdding(false); list.reload(); }} />}
      {list.data.agents.length === 0 && !adding && <Empty>No agents yet.{canEdit ? " Register one: its tools, and a schedule with a prompt or a notebook." : ""}</Empty>}
      {list.data.agents.map((a) => (
        <div key={a.name} className="card stack" style={{ gap: 8 }} data-testid={`agent-${a.name}`}>
          <div className="card-head">
            <h2 className="mono">{a.name}<span className={"pill " + (a.approved ? "green" : "amber")}>{a.approved ? `approved by ${a.approvedBy}` : "awaiting approval"}</span></h2>
            <div className="row">
            {canApprove && !a.approved && a.registeredBy !== user.username && (
              <button className="btn small primary" data-testid={`approve-${a.name}`}
                onClick={() => act(async () => { const r = await approveAgent(name, a.name); if (r.secret) setSecret({ agent: a.name, clientId: r.clientId, secret: r.secret, mcpUrl: r.mcpUrl }); })}>Approve</button>
            )}
            {canEdit && <button className="btn small danger" onClick={() => { if (confirm(`Revoke ${a.name}? It signs in no more.`)) act(() => removeAgent(name, a.name)); }} data-testid={`revoke-${a.name}`}>Revoke</button>}
            </div>
          </div>
          <div className="small">
            {a.description && <>{a.description} </>}
            Owner <span className="mono">{a.owner}</span>; client <span className="mono">{a.clientId}</span>; tools: <span className="mono">{(a.tools ?? []).join(", ") || "every tool a reader may use"}</span>
            {a.schedule && <>; runs <span className="mono">{a.notebook || "its prompt"}</span> at <span className="mono">{a.schedule}</span> (UTC){a.dag ? <> as the pipeline <Link to={`/projects/${encodeURIComponent(name)}/pipelines`} className="mono">{a.dag}</Link></> : ", once approved"}</>}
            {a.budget ? <>; {useOf(a.name)?.key ? `${useOf(a.name)!.spend.toFixed(2)} of` : "a budget of"} {a.budget} USD a month at the gateway</> : null}.
            {a.registeredBy && <> Registered by {a.registeredBy}.</>}
          </div>
        </div>
      ))}
      {canEdit && llm.data?.gateway && llm.data.uses.some((u) => u.key) && <ModelCalls project={name} />}
      {knowledge.data && <Knowledge project={name} info={knowledge.data} canBuild={knowledge.data.canBuild || admin} reload={knowledge.reload} />}
      {llm.data?.gateway && (
        <div className="card stack" style={{ gap: 8 }} data-testid="llm">
          <h2>The LLM gateway</h2>
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
    </div>
  );
}

function AgentForm({ project, tools, taken, owner, onDone }: { project: string; tools: string[]; taken: string[]; owner: string; onDone: () => void }) {
  const [agentName, setName] = useState("");
  const [description, setDescription] = useState("");
  const [picked, setPicked] = useState<string[]>(["list_metrics", "query_metrics"]);
  const [schedule, setSchedule] = useState("");
  const [notebook, setNotebook] = useState("");
  const [prompt, setPrompt] = useState("");
  const [budget, setBudget] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const ok = /^[a-z][a-z0-9-]{0,30}$/.test(agentName) && !taken.includes(agentName);
  const save = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await putAgent(project, { name: agentName, description: description || undefined, owner, tools: picked, schedule: schedule || undefined, notebook: notebook || undefined, prompt: prompt || undefined, budget: budget ? Number(budget) : undefined });
      onDone();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <form className="card stack" onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="agent-form">
      <h2>Register an agent</h2>
      <ErrorBanner error={error} />
      <div className="fields">
        <label>Name<input className="mono" value={agentName} onChange={(e) => setName(e.target.value)} placeholder="nightly-summary" data-testid="agent-name" /></label>
        <label className="grow">What it's for<input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="A morning summary of the open alerts" /></label>
      </div>
      <div className="fields">
        <label>Schedule<input className="mono" value={schedule} onChange={(e) => setSchedule(e.target.value)} placeholder="0 6 * * * (UTC), or none" data-testid="agent-schedule" /></label>
        <label className="grow">Notebook<input className="mono" value={notebook} onChange={(e) => setNotebook(e.target.value)} placeholder="notebooks/nightly.ipynb, in the repo" data-testid="agent-notebook" /></label>
        <label>LLM budget (USD a month)<input className="mono" value={budget} onChange={(e) => setBudget(e.target.value)} placeholder="none" data-testid="agent-budget" /></label>
      </div>
      <label className="grow">Or, in place of a notebook, what it's to do<textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} rows={8}
        style={{ resize: "vertical" }}
        placeholder="Each morning, read the open alerts and the failed runs, and write a two-sentence summary for the team." data-testid="agent-prompt" /></label>
      <div className="field-help"><strong>Tools it may use</strong> (of the MCP server's); none picked means every tool a reader may use. <span className="mono">tool:</span> is one of the project's own tools (an API's operation, signed in by the project's connection, which the agent never sees); <span className="mono">agent:</span> is another agent with a prompt, which it may ask to do a task (that agent does it as itself, and isn't given agents in turn). With a schedule and a prompt (and a budget), the platform's runner does the prompt as the agent: the model is given its tools, each tool it asks for is called as the agent, and the run's record is the transcript. With a schedule and a notebook (an .ipynb in the project's repo, under the pipelines' path), the platform runs the notebook as the agent: <span className="mono">buckets_lake.agent()</span> is the MCP server, <span className="mono">buckets_lake.ask()</span> the gateway, on its budget.</div>
      <div className="chips">
        {tools.map((t) => <button key={t} type="button" className={`chip${picked.includes(t) ? " on" : ""}`} aria-pressed={picked.includes(t)}
          onClick={() => setPicked(picked.includes(t) ? picked.filter((x) => x !== t) : [...picked, t])} data-testid={`tool-${t}`}>{t}</button>)}
      </div>
      <div className="form-actions">
        <button className="btn primary" type="submit" disabled={!ok || busy} data-testid="register-agent">{busy ? "Saving…" : "Register"}</button>
        <button className="btn" type="button" onClick={onDone}>Cancel</button>
        <span className="field-help">Owner: {owner}. Another editor approves it before it may sign in.</span>
      </div>
    </form>
  );
}

// Knowledge banks (use case 12.6): the project's text, searched by meaning, as the person; built by its editors.
function Knowledge({ project, info, canBuild, reload }: { project: string; info: Awaited<ReturnType<typeof getKnowledge>>; canBuild: boolean; reload: () => void }) {
  const banks = info.banks;
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState<string>();
  const [error, setError] = useState<string>();
  const [note, setNote] = useState<string>();
  const [bank, setBank] = useState(banks[0]?.name ?? "");
  const remove = async (b: string) => {
    if (!window.confirm(`Take the bank ${b} out of the project? Its table knowledge_${b} stays until it's dropped.`)) return;
    setBusy(b); setError(undefined);
    try { await deleteKnowledge(project, b); reload(); } catch (e) { setError((e as Error).message); } finally { setBusy(undefined); }
  };
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<KnowledgeHit[]>();
  const build = async (b: string) => {
    setBusy(b); setError(undefined); setNote(undefined);
    try {
      const r = await buildKnowledge(project, b);
      setNote(`${b}: ${r.chunks} passages embedded${r.files ? ` (${r.files} files${r.skipped ? `, ${r.skipped} skipped: not .txt, .md or .html, or too big` : ""})` : ""}, ${r.cost.toFixed(4)} spent.`);
      reload();
    }
    catch (e) { setError((e as Error).message); } finally { setBusy(undefined); }
  };
  const search = async () => {
    setBusy("search"); setError(undefined);
    try { setHits((await searchKnowledge(project, bank || banks[0].name, query)).hits); } catch (e) { setError((e as Error).message); } finally { setBusy(undefined); }
  };
  return (
    <div className="card stack" style={{ gap: 8 }} data-testid="knowledge">
      <div className="card-head"><h2>Knowledge banks</h2>
        {canBuild && !adding && <button className="btn small" onClick={() => setAdding(true)} data-testid="add-bank">Add a bank</button>}</div>
      <div className="small muted">The project's text (a table's text column, or the files in a folder of its bucket), embedded through the gateway on its key and kept as a table of the project's; searched by meaning, as you, by agents with the tool <span className="mono">search_documents</span>, and on the canvas (Ask a model, nearest passage).</div>
      <ErrorBanner error={error} />
      {note && <div className="small">{note}</div>}
      {!info.hasBudget && <div className="small muted">A bank is embedded on the project's gateway key: the project needs an LLM budget first (an administrator sets it on Edit).</div>}
      {adding && <BankForm project={project} info={info} taken={banks.map((b) => b.name)} onDone={() => { setAdding(false); reload(); }} />}
      {banks.length === 0 && !adding && <Empty inline>No banks yet.{canBuild ? " Add one: its sources, and whether to build it on a schedule." : ""}</Empty>}
      {banks.map((b) => (
        <div key={b.name} className="row small" data-testid={`bank-${b.name}`}>
          <span className="mono">{b.name}</span>
          <span className="muted">{[...(b.tables ?? []).map((t) => `${t.table}.${t.column}`), ...(b.files ?? []).map((f) => `files in ${f.prefix}`)].join(", ")}</span>
          {b.schedule && <span className="muted">built at <span className="mono">{b.schedule}</span> (UTC)</span>}
          <span>{b.chunks === undefined ? "not built" : `${b.chunks} passages`}</span>
          {canBuild && <button className="btn small" disabled={!!busy} onClick={() => build(b.name)}>{busy === b.name ? "Building…" : b.chunks === undefined ? "Build" : "Build again"}</button>}
          {canBuild && <button className="btn small" disabled={!!busy} onClick={() => remove(b.name)}>Remove</button>}
        </div>
      ))}
      {banks.length > 0 && <div className="row">
        <select value={bank || banks[0].name} onChange={(e) => setBank(e.target.value)} aria-label="Bank">{banks.map((b) => <option key={b.name}>{b.name}</option>)}</select>
        <input className="grow" value={query} placeholder="Search by meaning: early termination terms" onChange={(e) => setQuery(e.target.value)} onKeyDown={(e) => e.key === "Enter" && query && search()} data-testid="knowledge-query" />
        <button className="btn" disabled={!query || !!busy} onClick={search}>Search</button>
      </div>}
      {hits && (hits.length === 0 ? <div className="small muted">Nothing found.</div> : (
        <table className="t" data-testid="knowledge-hits">
          <thead><tr><th>Score</th><th>From</th><th>Passage</th></tr></thead>
          <tbody>{hits.map((h, i) => <tr key={i}><td className="mono">{h.score.toFixed(2)}</td><td className="mono small">{h.source} #{h.ref}</td><td className="small">{h.text}</td></tr>)}</tbody>
        </table>
      ))}
    </div>
  );
}

// A new bank: its name, its sources (tables' text columns, folders of the project's bucket), and a schedule.
function BankForm({ project, info, taken, onDone }: { project: string; info: Awaited<ReturnType<typeof getKnowledge>>; taken: string[]; onDone: () => void }) {
  const [bankName, setName] = useState("");
  const [description, setDescription] = useState("");
  const [tables, setTables] = useState([{ table: "", column: "", id: "" }]);
  const [folder, setFolder] = useState("");
  const [schedule, setSchedule] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const used = tables.filter((t) => t.table || t.column || t.id);
  const ok = /^[a-z_][a-z0-9_]{0,49}$/.test(bankName) && !taken.includes(bankName) && (used.length > 0 || folder.trim() !== "");
  const save = async () => {
    setBusy(true); setError(undefined);
    try {
      const prefix = folder.trim() && (folder.trim().endsWith("/") ? folder.trim() : folder.trim() + "/");
      await putKnowledge(project, bankName, { description: description || undefined, tables: used.length ? used : undefined,
        files: prefix ? [{ prefix }] : undefined, schedule: schedule || undefined });
      onDone();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <form className="subcard stack" onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="bank-form">
      <h2>Add a knowledge bank</h2>
      <ErrorBanner error={error} />
      <div className="fields">
        <label>Name<input className="mono" value={bankName} onChange={(e) => setName(e.target.value)} placeholder="reviews" data-testid="bank-name" /></label>
        <label className="grow">What it holds<input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What customers wrote" /></label>
      </div>
      <div className="stack" style={{ gap: 8 }}>
      <div className="field-label">Tables: a text column, and the column that names each row (cited with each passage)</div>
      {tables.map((t, i) => (
        <div key={i} className="fields">
          <input className="mono" value={t.table} placeholder="table" onChange={(e) => setTables(tables.map((x, j) => j === i ? { ...x, table: e.target.value } : x))} aria-label="Table" />
          <input className="mono" value={t.column} placeholder="text column" onChange={(e) => setTables(tables.map((x, j) => j === i ? { ...x, column: e.target.value } : x))} aria-label="Text column" />
          <input className="mono" value={t.id} placeholder="id column" onChange={(e) => setTables(tables.map((x, j) => j === i ? { ...x, id: e.target.value } : x))} aria-label="Id column" />
          {tables.length > 1 && <button type="button" className="btn" onClick={() => setTables(tables.filter((_, j) => j !== i))}>Remove</button>}
        </div>
      ))}
      <div><button type="button" className="btn small" onClick={() => setTables([...tables, { table: "", column: "", id: "" }])}>Add a table</button></div>
      </div>
      <div className="fields">
      <label className="grow">And/or a folder of the project's bucket (its .txt, .md and .html files)
        <input className="mono" value={folder} onChange={(e) => setFolder(e.target.value)} placeholder={info.hasFiles ? "landing/uploads/" : "the project has no bucket (files)"} disabled={!info.hasFiles} data-testid="bank-folder" /></label>
      <label className="grow">Build it afresh on a schedule
        <input className="mono" value={schedule} onChange={(e) => setSchedule(e.target.value)} placeholder={info.hasPipelines ? "0 3 * * * (UTC), or by hand" : "by hand (a schedule needs the project's pipelines)"} disabled={!info.hasPipelines} data-testid="bank-schedule" /></label>
      </div>
      <div className="form-actions">
        <button className="btn primary" type="submit" disabled={!ok || busy} data-testid="save-bank">{busy ? "Saving…" : "Add"}</button>
        <button className="btn" type="button" onClick={onDone}>Cancel</button>
        <span className="field-help">Then press Build: nothing is embedded until it's built.</span>
      </div>
    </form>
  );
}

// The project's calls of the gateway (use case 12.8), to its editors: who asked, with which key, the model,
// tokens, cost and time; never the question or the answer.
function ModelCalls({ project }: { project: string }) {
  const [days, setDays] = useState(7);
  const calls = usePoll(() => getLLMCalls(project, days), [project, days], 60_000);
  const d = calls.data;
  const money = (x: number) => x < 0.01 ? x.toFixed(4) : x.toFixed(2);
  const top = (m?: Record<string, { calls: number; cost: number }>) => Object.entries(m ?? {}).sort((a, b) => b[1].cost - a[1].cost).slice(0, 5);
  return (
    <div className="card stack" style={{ gap: 8 }} data-testid="llm-calls">
      <div className="card-head"><h2>Model calls</h2>
        <select value={days} onChange={(e) => setDays(Number(e.target.value))} aria-label="Days">{[1, 7, 31].map((n) => <option key={n} value={n}>last {n} day{n > 1 ? "s" : ""}</option>)}</select></div>
      {calls.error && <ErrorBanner error={calls.error} />}
      {!d ? <Spinner /> : <>
        <div className="small">{d.total.calls} calls, {d.total.tokens} tokens, {money(d.total.cost)} USD.
          {top(d.byUser).length > 0 && <> By who asked: {top(d.byUser).map(([u, t]) => `${u || "unnamed"} ${money(t.cost)}`).join(", ")}.</>}
          {top(d.byModel).length > 0 && <> By model: {top(d.byModel).map(([m, t]) => `${m} ${t.calls}`).join(", ")}.</>}</div>
        {d.calls.length > 0 && (
          <div className="scroll-x"><table className="t">
            <thead><tr><th>When</th><th>Key</th><th>Who asked</th><th>Model</th><th>Tokens</th><th>Cost</th><th>Seconds</th><th></th></tr></thead>
            <tbody>{d.calls.slice(0, 50).map((c, i) => (
              <tr key={i}><td className="small">{new Date(c.at).toLocaleString()}</td><td className="mono small">{c.who === project ? "the project" : c.who}</td>
                <td className="mono small">{c.user || "–"}</td><td className="mono small">{c.model} <span className="muted">{c.kind}</span></td>
                <td>{c.tokens}</td><td>{money(c.cost)}</td><td>{c.seconds.toFixed(1)}</td>
                <td>{c.status !== "success" && <span className="pill red">{c.status}</span>}</td></tr>))}</tbody>
          </table></div>)}
        <div className="small muted">As the gateway records them: the question and the answer are never kept.</div>
      </>}
    </div>
  );
}
