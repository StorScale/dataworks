import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  Background, Controls, Handle, Position, ReactFlow, type Connection, type Edge, type EdgeChange, type Node, type NodeChange, type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  compileFlow, getFlowView, getFlows, previewFlow, saveFlow, type CanvasFlow, type StepColumn, type FlowItem, type FlowPreview, type FlowStep, type FlowView,
} from "../api";
import { ErrorBanner, Spinner, usePlatform, usePoll, useProject } from "../lib";
import { Results } from "./Data";

// Flows is a project's pipelines built on a canvas (use cases 11.1 and
// 11.2): each a folder of dbt models in the project's repo, one per step,
// which the canvas reads and writes. Each step's columns and SQL come from
// the platform's compiler before anything runs; a preview runs the steps up
// to one on a sample, as you. Saving commits the models on a branch with a
// merge request, reviewed and released like any other change.

const KINDS: [string, string][] = [
  ["input", "Input"], ["select", "Select and rename"], ["retype", "Retype"], ["filter", "Filter"], ["join", "Join"], ["union", "Union"],
  ["dedupe", "Deduplicate"], ["group", "Group by"], ["pivot", "Pivot"], ["unpivot", "Unpivot"], ["window", "Window"], ["formula", "Formula"], ["output", "Output"],
];
const label = (kind: string) => KINDS.find((k) => k[0] === kind)?.[1] ?? (kind === "code" ? "Code" : kind);
const AGGREGATES = ["sum", "count", "min", "max", "avg", "count_distinct"];
const WINDOWS = ["rank", "dense_rank", "row_number", "running_sum", "sum", "avg", "min", "max", "count", "lag", "lead"];
const TESTS = ["not_null", "unique", "accepted_values"];
const inputsOf = (kind: string) => (kind === "input" ? 0 : kind === "join" ? 2 : kind === "union" || kind === "code" ? 99 : 1);

type StepNodeData = { step: FlowStep; columns?: StepColumn[] | null; error?: string; unknown?: boolean };

function StepNode({ data, selected }: NodeProps<Node<StepNodeData>>) {
  const { step } = data;
  const cols = data.columns?.length;
  return (
    <div className={"stepnode" + (selected ? " selected" : "") + (data.error ? " alert" : "")} data-testid={`step-${step.id}`}>
      {step.kind !== "input" && <Handle type="target" position={Position.Left} />}
      <div className="kind">{label(step.kind)}</div>
      <div className="name">{step.kind === "input" ? step.table || step.id : step.kind === "output" ? step.table || step.id : step.id}</div>
      <div className="cols">{data.error ? "can't compile" : data.unknown ? "columns unknown" : cols !== undefined ? `${cols} columns` : ""}</div>
      {step.kind !== "output" && <Handle type="source" position={Position.Right} />}
    </div>
  );
}
const nodeTypes = { step: StepNode };

export default function Flows() {
  const { name, view } = useProject();
  const { admin } = usePlatform();
  const [params, setParams] = useSearchParams();
  const current = params.get("flow");
  const list = usePoll(() => getFlows(name), [name]);
  const [newName, setNewName] = useState("");
  const canEdit = view?.role === "editor" || admin;
  if (current) return <Canvas project={name} flowName={current} canEdit={canEdit} onClose={() => { setParams({}); list.reload(); }} />;
  if (list.error && !list.data) return <div className="page"><ErrorBanner error={list.error} /></div>;
  if (!list.data) return <div className="page"><Spinner /></div>;
  const flows = list.data.flows;
  return (
    <div className="page stack" data-testid="flows">
      <p className="lead" style={{ margin: 0 }}>
        Pipelines built on a canvas: each step a dbt model in the project's repo, previewed on a sample as you, saved as a merge request.
      </p>
      {list.data.why && <div className="banner" data-testid="flows-why">{list.data.why}</div>}
      {canEdit && !list.data.why && (
        <form className="row" onSubmit={(e) => { e.preventDefault(); if (/^[a-z][a-z0-9_]{0,40}$/.test(newName)) setParams({ flow: newName, new: "1" }); }}>
          <input placeholder="a new flow's name (weekly_revenue)" value={newName} onChange={(e) => setNewName(e.target.value)} data-testid="new-flow-name" />
          <button className="btn primary" type="submit" disabled={!/^[a-z][a-z0-9_]{0,40}$/.test(newName)} data-testid="new-flow">New flow</button>
        </form>
      )}
      {flows.length === 0 && <p className="muted">No flows yet.</p>}
      {flows.map((f: FlowItem) => (
        <div key={f.name} className="card row" data-testid={`flow-${f.name}`}>
          <Link to={`?flow=${f.name}`} className="mono">{f.name}</Link>
          {f.branch && <span className="pill">on {f.branch}</span>}
          {f.mergeRequest && <a href={f.mergeRequest} target="_blank" rel="noreferrer" className="small">merge request</a>}
        </div>
      ))}
    </div>
  );
}

// ids are the step names that are taken, so a new step gets a free one.
function freeId(kind: string, steps: FlowStep[]) {
  const base = kind === "output" ? "out" : kind;
  let i = 1;
  while (steps.some((s) => s.id === (i === 1 ? base : `${base}_${i}`))) i++;
  return i === 1 ? base : `${base}_${i}`;
}

function Canvas({ project, flowName, canEdit, onClose }: { project: string; flowName: string; canEdit: boolean; onClose: () => void }) {
  const [params] = useSearchParams();
  const [flow, setFlow] = useState<CanvasFlow>();
  const [compiled, setCompiled] = useState<FlowView>();
  const [loadError, setLoadError] = useState<string>();
  const [selected, setSelected] = useState<string>();
  const [preview, setPreview] = useState<FlowPreview>();
  const [previewError, setPreviewError] = useState<string>();
  const [previewing, setPreviewing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState<{ branch: string; mergeRequest: string }>();
  const [saveError, setSaveError] = useState<string>();
  const [adding, setAdding] = useState("");
  const [tab, setTab] = useState<"preview" | "sql">("preview");
  const timer = useRef<number>();

  useEffect(() => {
    if (params.get("new")) {
      setFlow({ name: flowName, steps: [] });
      setCompiled(undefined);
      return;
    }
    getFlowView(project, flowName).then((v) => {
      setFlow(v.flow);
      setCompiled(v);
      if (v.branch) setSaved({ branch: v.branch, mergeRequest: v.mergeRequest ?? "" });
    }).catch((e) => setLoadError((e as Error).message));
  }, [project, flowName]); // eslint-disable-line react-hooks/exhaustive-deps

  // Compiled again a moment after each change: columns, SQL, errors.
  const change = useCallback((next: CanvasFlow) => {
    setFlow(next);
    setPreview(undefined);
    setPreviewError(undefined);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      compileFlow(project, next).then(setCompiled).catch((e) => setCompiled((c) => c && { ...c, errors: [(e as Error).message] }));
    }, 400);
  }, [project]);

  const steps = flow?.steps ?? [];
  const nodes: Node<StepNodeData>[] = useMemo(() => steps.map((s) => ({
    id: s.id, type: "step", position: { x: s.x, y: s.y }, selected: s.id === selected,
    data: { step: s, columns: compiled?.steps[s.id]?.columns, error: compiled?.steps[s.id]?.error, unknown: compiled?.steps[s.id]?.unknown },
  })), [steps, compiled, selected]);
  const edges: Edge[] = useMemo(() => steps.flatMap((s) => (s.inputs ?? []).map((i) => ({ id: `${i}->${s.id}`, source: i, target: s.id }))), [steps]);

  const onNodesChange = (changes: NodeChange[]) => {
    if (!flow) return;
    let next = flow.steps;
    let moved = false;
    for (const c of changes) {
      if (c.type === "position" && c.position) {
        next = next.map((s) => (s.id === c.id ? { ...s, x: Math.round(c.position!.x), y: Math.round(c.position!.y) } : s));
        moved = true;
      } else if (c.type === "select") {
        if (c.selected) setSelected(c.id);
        else if (selected === c.id) setSelected(undefined);
      } else if (c.type === "remove" && canEdit) {
        next = next.filter((s) => s.id !== c.id).map((s) => ({ ...s, inputs: (s.inputs ?? []).filter((i) => i !== c.id) }));
        moved = true;
      }
    }
    if (moved) setFlow({ ...flow, steps: next });
  };
  const onEdgesChange = (changes: EdgeChange[]) => {
    if (!flow || !canEdit) return;
    let next = flow.steps;
    for (const c of changes) {
      if (c.type === "remove") {
        const [source, target] = c.id.split("->");
        next = next.map((s) => (s.id === target ? { ...s, inputs: (s.inputs ?? []).filter((i) => i !== source) } : s));
      }
    }
    if (next !== flow.steps) change({ ...flow, steps: next });
  };
  const onConnect = (c: Connection) => {
    if (!flow || !canEdit || !c.source || !c.target || c.source === c.target) return;
    change({ ...flow, steps: flow.steps.map((s) => {
      if (s.id !== c.target) return s;
      const max = inputsOf(s.kind);
      const inputs = (s.inputs ?? []).filter((i) => i !== c.source);
      return { ...s, inputs: max === 1 ? [c.source!] : [...inputs, c.source!].slice(-max) };
    }) });
  };
  const addStep = (kind: string) => {
    if (!flow) return;
    const from = steps.find((s) => s.id === selected);
    const id = freeId(kind, steps);
    const step: FlowStep = { id, kind, x: from ? from.x + 220 : 40 + (steps.length % 4) * 220, y: from ? from.y : 40 + Math.floor(steps.length / 4) * 120 };
    if (from && inputsOf(kind) > 0 && from.kind !== "output") step.inputs = [from.id];
    if (kind === "join") step.join = "inner";
    if (kind === "output") step.table = flowName;
    change({ ...flow, steps: [...steps, step] });
    setSelected(id);
    setAdding("");
  };
  const update = (step: FlowStep) => flow && change({ ...flow, steps: flow.steps.map((s) => (s.id === step.id ? step : s)) });
  const remove = (id: string) => flow && change({ ...flow, steps: flow.steps.filter((s) => s.id !== id).map((s) => ({ ...s, inputs: (s.inputs ?? []).filter((i) => i !== id) })) });
  const runPreview = async () => {
    if (!flow || !selected) return;
    setPreviewing(true);
    setPreviewError(undefined);
    try {
      setPreview(await previewFlow(project, flow, selected));
      setTab("preview");
    } catch (e) {
      setPreviewError((e as Error).message);
      setPreview(undefined);
    } finally {
      setPreviewing(false);
    }
  };
  const save = async () => {
    if (!flow) return;
    setSaving(true);
    setSaveError(undefined);
    try {
      const r = await saveFlow(project, flow);
      setSaved({ branch: r.branch, mergeRequest: r.mergeRequest });
    } catch (e) {
      setSaveError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  if (loadError) return <div className="page"><ErrorBanner error={loadError} /><button className="btn" onClick={onClose}>Back to the flows</button></div>;
  if (!flow) return <div className="page"><Spinner /></div>;
  const step = steps.find((s) => s.id === selected);
  const result = step && compiled?.steps[step.id];
  const errors = compiled?.errors ?? [];
  const inputColumns = (s: FlowStep) => (s.inputs ?? []).map((i) => compiled?.steps[i]?.columns ?? []);
  return (
    <div className="page stack" data-testid="canvas">
      <div className="row">
        <button className="btn small" onClick={onClose}>← Flows</button>
        <h2 className="mono" style={{ margin: 0 }}>{flow.name}</h2>
        {saved?.branch && <span className="pill">on {saved.branch}</span>}
        {saved?.mergeRequest && <a href={saved.mergeRequest} target="_blank" rel="noreferrer" className="small" data-testid="merge-request">merge request</a>}
        <span className="grow" />
        {canEdit && (
          <select value={adding} onChange={(e) => addStep(e.target.value)} data-testid="add-step" aria-label="Add a step">
            <option value="">Add a step…</option>
            {KINDS.map(([k, l]) => <option key={k} value={k}>{l}</option>)}
          </select>
        )}
        {canEdit && (
          <label className="row small" style={{ gap: 6 }}>Schedule
            <input value={flow.schedule ?? ""} placeholder="0 6 * * 1 (UTC), or none" style={{ width: 170 }} onChange={(e) => change({ ...flow, schedule: e.target.value })} data-testid="schedule" />
          </label>
        )}
        {canEdit && <button className="btn primary" onClick={save} disabled={saving || errors.length > 0 || steps.length === 0} data-testid="save-flow">{saving ? "Saving…" : "Save as merge request"}</button>}
      </div>
      <ErrorBanner error={saveError} />
      {errors.length > 0 && <div className="banner error" data-testid="flow-errors">{errors.map((e) => <div key={e}>{e}</div>)}</div>}
      <div className="split">
        <div className="main canvas" style={{ flex: "999 1 480px" }}>
          <ReactFlow nodes={nodes} edges={edges} nodeTypes={nodeTypes} onNodesChange={onNodesChange} onEdgesChange={onEdgesChange} onConnect={onConnect}
            nodesDraggable={canEdit} nodesConnectable={canEdit} elementsSelectable fitView deleteKeyCode={canEdit ? ["Backspace", "Delete"] : null} proOptions={{ hideAttribution: true }}>
            <Background />
            <Controls showInteractive={false} />
          </ReactFlow>
        </div>
        <div className="flowside card" data-testid="step-settings">
          {!step && <p className="muted small" style={{ margin: 0 }}>{steps.length === 0 ? "Add an input to start." : "Pick a step to see its settings."}</p>}
          {step && (
            <Settings key={step.id} step={step} steps={steps} inputs={inputColumns(step)} columns={result?.columns ?? undefined} canEdit={canEdit} update={update}
              remove={() => { remove(step.id); setSelected(undefined); }} />
          )}
          {step && result?.error && <div className="banner error small" data-testid="step-error">{result.error}</div>}
          {step && <div className="row" style={{ marginTop: 8 }}>
            <button className="btn" onClick={runPreview} disabled={previewing || !!result?.error} data-testid="preview">{previewing ? "Running…" : "Preview"}</button>
            <button className={"btn small" + (tab === "sql" ? " primary" : "")} onClick={() => setTab(tab === "sql" ? "preview" : "sql")}>SQL</button>
          </div>}
        </div>
      </div>
      {step && tab === "sql" && (
        <div className="card">
          <p className="small muted" style={{ margin: "0 0 6px" }}>The step's model, <span className="mono">{result?.model}.sql</span>; the preview runs the steps up to it as one query.</p>
          <pre className="small" style={{ whiteSpace: "pre-wrap", margin: 0 }} data-testid="step-sql">{preview?.sql ?? result?.sql ?? step.sql}</pre>
        </div>
      )}
      {step && tab === "preview" && previewError && <div className="banner error" data-testid="preview-error">{previewError}</div>}
      {step && tab === "preview" && preview && (
        <div className="card" style={{ padding: 0 }}>
          <Results answer={preview} testid="preview-rows" note={preview.sampled?.length ? `A sample of ${preview.sampled.join(", ")}.` : undefined} />
        </div>
      )}
      {step && tab === "preview" && !preview && result?.columns && (
        <p className="small muted" data-testid="step-columns">Columns: {result.columns.map((c) => `${c.name}${c.type ? ` (${c.type})` : ""}`).join(", ") || "none"}</p>
      )}
    </div>
  );
}

// --- A step's settings --------------------------------------------------------------

type SettingsProps = { step: FlowStep; steps: FlowStep[]; inputs: StepColumn[][]; columns?: StepColumn[]; canEdit: boolean; update: (s: FlowStep) => void; remove: () => void };

const list = (v?: string[]) => (v ?? []).join(", ");
const split = (v: string) => v.split(",").map((x) => x.trim()).filter(Boolean);

function Settings({ step, steps, inputs, canEdit, update, remove }: SettingsProps) {
  const set = (patch: Partial<FlowStep>) => update({ ...step, ...patch });
  const ro = !canEdit;
  const cols = inputs[0] ?? [];
  const names = cols.map((c) => c.name);
  const ColumnPick = ({ value, onChange, allow = names }: { value?: string; onChange: (v: string) => void; allow?: string[] }) => (
    <select value={value ?? ""} onChange={(e) => onChange(e.target.value)} disabled={ro}>
      <option value="">column…</option>
      {allow.map((n) => <option key={n} value={n}>{n}</option>)}
    </select>
  );
  const Rows = <T,>({ items, blank, render, onChange, add }: { items: T[]; blank: T; render: (it: T, set: (it: T) => void) => React.ReactNode; onChange: (items: T[]) => void; add: string }) => (
    <div className="rows">
      {items.map((it, i) => (
        <div key={i} className="row">
          {render(it, (n) => onChange(items.map((x, j) => (j === i ? n : x))))}
          {!ro && <button type="button" className="btn small" onClick={() => onChange(items.filter((_, j) => j !== i))} aria-label="Remove">×</button>}
        </div>
      ))}
      {!ro && <button type="button" className="btn small" onClick={() => onChange([...items, blank])}>{add}</button>}
    </div>
  );
  const others = steps.filter((s) => s.id !== step.id && s.kind !== "output").map((s) => s.id);
  return (
    <div>
      <div className="row" style={{ marginBottom: 8 }}>
        <strong>{label(step.kind)}</strong>
        <span className="mono small">{step.id}</span>
        <span className="grow" />
        {canEdit && <button className="btn small" onClick={remove} data-testid="remove-step">Remove</button>}
      </div>
      {step.kind !== "input" && (
        <label>Inputs
          <input value={list(step.inputs)} readOnly={ro} onChange={(e) => set({ inputs: split(e.target.value).slice(0, inputsOf(step.kind)) })} list={`steps-${step.id}`} placeholder={inputsOf(step.kind) > 1 ? "step, step" : "a step"} data-testid="inputs" />
          <datalist id={`steps-${step.id}`}>{others.map((o) => <option key={o} value={o} />)}</datalist>
        </label>
      )}
      <label>Description<input value={step.description ?? ""} readOnly={ro} onChange={(e) => set({ description: e.target.value })} /></label>
      {step.kind === "input" && <>
        <label>Table<input value={step.table ?? ""} readOnly={ro} onChange={(e) => set({ table: e.target.value })} placeholder="orders" data-testid="input-table" /></label>
        <label>Namespace (another project's, for a product it shares)<input value={step.namespace ?? ""} readOnly={ro} onChange={(e) => set({ namespace: e.target.value || undefined })} placeholder="the project's own" /></label>
      </>}
      {step.kind === "select" && <>
        <span className="small muted">Kept columns, in order, each with its new name if renamed.</span>
        <Rows items={step.columns ?? []} blank={{ name: "" }} onChange={(columns) => set({ columns })} add="Keep a column"
          render={(c, s) => <><ColumnPick value={c.name} onChange={(name) => s({ ...c, name })} /><input value={c.as ?? ""} readOnly={ro} placeholder="as" onChange={(e) => s({ ...c, as: e.target.value || undefined })} /></>} />
        {!ro && <button type="button" className="btn small" onClick={() => set({ columns: names.map((name) => ({ name })) })}>Keep all</button>}
      </>}
      {step.kind === "retype" && (
        <Rows items={step.columns ?? []} blank={{ name: "", type: "varchar" }} onChange={(columns) => set({ columns })} add="Cast a column"
          render={(c, s) => <><ColumnPick value={c.name} onChange={(name) => s({ ...c, name })} /><input value={c.type ?? ""} readOnly={ro} placeholder="bigint, varchar, date, decimal(10,2)" onChange={(e) => s({ ...c, type: e.target.value })} /></>} />
      )}
      {step.kind === "filter" && <label>Keep rows where<textarea value={step.where ?? ""} readOnly={ro} rows={2} onChange={(e) => set({ where: e.target.value })} placeholder="amount > 0 and not test" data-testid="where" /></label>}
      {step.kind === "join" && <>
        <label>Kind<select value={step.join ?? "inner"} disabled={ro} onChange={(e) => set({ join: e.target.value })}><option value="inner">inner</option><option value="left">left</option><option value="full">full</option></select></label>
        <span className="small muted">On: a column of {step.inputs?.[0] ?? "the left"} equal to one of {step.inputs?.[1] ?? "the right"}.</span>
        <Rows items={step.on ?? []} blank={{ left: "", right: "" }} onChange={(on) => set({ on })} add="Add a pair"
          render={(p, s) => <><ColumnPick value={p.left} onChange={(left) => s({ ...p, left })} /><ColumnPick value={p.right} allow={(inputs[1] ?? []).map((c) => c.name)} onChange={(right) => s({ ...p, right })} /></>} />
      </>}
      {step.kind === "union" && <label className="row" style={{ flexDirection: "row", gap: 8 }}><input type="checkbox" checked={!!step.distinct} disabled={ro} onChange={(e) => set({ distinct: e.target.checked })} />Drop duplicate rows (union, not union all)</label>}
      {step.kind === "dedupe" && <>
        <label>Same when these columns are equal (none: whole rows)<input value={list(step.by)} readOnly={ro} onChange={(e) => set({ by: split(e.target.value) })} placeholder="order_id" /></label>
        <span className="small muted">Keep the first by:</span>
        <Rows items={step.order ?? []} blank={{ column: "" }} onChange={(order) => set({ order })} add="Order by"
          render={(o, s) => <><ColumnPick value={o.column} onChange={(column) => s({ ...o, column })} /><label className="row" style={{ flexDirection: "row", margin: 0 }}><input type="checkbox" checked={!!o.desc} disabled={ro} onChange={(e) => s({ ...o, desc: e.target.checked })} />desc</label></>} />
      </>}
      {step.kind === "group" && <>
        <label>Group by<input value={list(step.by)} readOnly={ro} onChange={(e) => set({ by: split(e.target.value) })} placeholder="region, week" data-testid="group-by" /></label>
        <Rows items={step.aggregates ?? []} blank={{ function: "sum", column: "" }} onChange={(aggregates) => set({ aggregates })} add="Add an aggregate"
          render={(a, s) => <>
            <select value={a.function} disabled={ro} onChange={(e) => s({ ...a, function: e.target.value })}>{AGGREGATES.map((f) => <option key={f}>{f}</option>)}</select>
            <ColumnPick value={a.column} onChange={(column) => s({ ...a, column })} />
            <input value={a.as ?? ""} readOnly={ro} placeholder="as" onChange={(e) => s({ ...a, as: e.target.value || undefined })} />
          </>} />
      </>}
      {step.kind === "pivot" && <>
        <label>Rows (group by)<input value={list(step.by)} readOnly={ro} onChange={(e) => set({ by: split(e.target.value) })} placeholder="week" /></label>
        <label>Column whose values become columns<ColumnPick value={step.column} onChange={(column) => set({ column })} /></label>
        <label>Its values<input value={list(step.values)} readOnly={ro} onChange={(e) => set({ values: split(e.target.value) })} placeholder="EU, US" /></label>
        <label>Value<ColumnPick value={step.value} onChange={(value) => set({ value })} /></label>
        <label>Aggregate<select value={step.function ?? "sum"} disabled={ro} onChange={(e) => set({ function: e.target.value })}>{["sum", "count", "min", "max", "avg"].map((f) => <option key={f}>{f}</option>)}</select></label>
      </>}
      {step.kind === "unpivot" && <>
        <label>Columns that become rows<input value={list(step.unpivot)} readOnly={ro} onChange={(e) => set({ unpivot: split(e.target.value) })} placeholder="eu, us" /></label>
        <label>Name column<input value={step.name ?? ""} readOnly={ro} onChange={(e) => set({ name: e.target.value || undefined })} placeholder="name" /></label>
        <label>Value column<input value={step.value ?? ""} readOnly={ro} onChange={(e) => set({ value: e.target.value || undefined })} placeholder="value" /></label>
      </>}
      {step.kind === "window" && <>
        <label>Partition by<input value={list(step.partition)} readOnly={ro} onChange={(e) => set({ partition: split(e.target.value) })} placeholder="region" /></label>
        <Rows items={step.order ?? []} blank={{ column: "" }} onChange={(order) => set({ order })} add="Order by"
          render={(o, s) => <><ColumnPick value={o.column} onChange={(column) => s({ ...o, column })} /><label className="row" style={{ flexDirection: "row", margin: 0 }}><input type="checkbox" checked={!!o.desc} disabled={ro} onChange={(e) => s({ ...o, desc: e.target.checked })} />desc</label></>} />
        <Rows items={step.windows ?? []} blank={{ function: "rank", as: "" }} onChange={(windows) => set({ windows })} add="Add a column"
          render={(w, s) => <>
            <select value={w.function} disabled={ro} onChange={(e) => s({ ...w, function: e.target.value })}>{WINDOWS.map((f) => <option key={f}>{f}</option>)}</select>
            {!["rank", "dense_rank", "row_number"].includes(w.function) && <ColumnPick value={w.column} onChange={(column) => s({ ...w, column })} />}
            <input value={w.as} readOnly={ro} placeholder="as" onChange={(e) => s({ ...w, as: e.target.value })} />
          </>} />
      </>}
      {step.kind === "formula" && <>
        <span className="small muted">Each a Trino expression; a type makes the column's type known to the steps after.</span>
        <Rows items={step.columns ?? []} blank={{ name: "", expression: "" }} onChange={(columns) => set({ columns })} add="Add a column"
          render={(c, s) => <>
            <input value={c.name} readOnly={ro} placeholder="name" onChange={(e) => s({ ...c, name: e.target.value })} />
            <input value={c.expression ?? ""} readOnly={ro} placeholder="amount * 1.2" onChange={(e) => s({ ...c, expression: e.target.value })} style={{ flex: "2 1 120px" }} />
            <input value={c.type ?? ""} readOnly={ro} placeholder="type" onChange={(e) => s({ ...c, type: e.target.value || undefined })} style={{ flex: "0 1 70px" }} />
          </>} />
      </>}
      {step.kind === "output" && <>
        <label>Table<input value={step.table ?? ""} readOnly={ro} onChange={(e) => set({ table: e.target.value })} placeholder="weekly_revenue" data-testid="output-table" /></label>
        <span className="small muted">Checks, run as dbt tests each time it's built:</span>
        <Rows items={step.checks ?? []} blank={{ column: "", test: "not_null" }} onChange={(checks) => set({ checks })} add="Add a check"
          render={(c, s) => <>
            <ColumnPick value={c.column} onChange={(column) => s({ ...c, column })} />
            <select value={c.test} disabled={ro} onChange={(e) => s({ ...c, test: e.target.value })}>{TESTS.map((t) => <option key={t}>{t}</option>)}</select>
            {c.test === "accepted_values" && <input value={list(c.values)} readOnly={ro} placeholder="values" onChange={(e) => s({ ...c, values: split(e.target.value) })} />}
          </>} />
      </>}
      {step.kind === "code" && <>
        <span className="small muted">A model written by hand: it stays code. Its inputs are its refs; its columns are what Trino says it returns.</span>
        <textarea value={step.sql ?? ""} readOnly={ro} rows={8} className="mono" onChange={(e) => set({ sql: e.target.value })} data-testid="code-sql" />
      </>}
    </div>
  );
}
