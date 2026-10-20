import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  Background, Controls, Handle, Position, ReactFlow, type Connection, type Edge, type EdgeChange, type Node, type NodeChange, type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  compileFlow, getFlow, getFlowView, getFlows, getSemantic, getUploadPreview, getUploads, previewFlow, putImport, removeUpload, saveFlow, uploadFile,
  type CanvasFlow, type StepColumn, type FlowItem, type FlowPreview, type FlowStep, type FlowTable, type FlowView, type SemanticMetric, type Upload, type UploadPreview,
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
  ["dedupe", "Deduplicate"], ["group", "Group by"], ["pivot", "Pivot"], ["unpivot", "Unpivot"], ["window", "Window"], ["formula", "Formula"], ["metric", "Metric"], ["output", "Output"],
];
const label = (kind: string) => KINDS.find((k) => k[0] === kind)?.[1] ?? (kind === "code" ? "Code" : kind);
const AGGREGATES = ["sum", "count", "min", "max", "avg", "count_distinct"];
const WINDOWS = ["rank", "dense_rank", "row_number", "running_sum", "sum", "avg", "min", "max", "count", "lag", "lead"];
const TESTS = ["not_null", "unique", "accepted_values"];
const TYPES = ["varchar", "bigint", "integer", "double", "decimal(10,2)", "boolean", "date", "timestamp", "timestamp(6)"];
const inputsOf = (kind: string) => (kind === "input" || kind === "metric" ? 0 : kind === "join" ? 2 : kind === "union" || kind === "code" ? 99 : 1);

type StepNodeData = { step: FlowStep; columns?: StepColumn[] | null; error?: string; unknown?: boolean };

function StepNode({ data, selected }: NodeProps<Node<StepNodeData>>) {
  const { step } = data;
  const cols = data.columns?.length;
  return (
    <div className={"stepnode" + (selected ? " selected" : "") + (data.error ? " alert" : "")} data-testid={`step-${step.id}`}>
      {inputsOf(step.kind) > 0 && <Handle type="target" position={Position.Left} />}
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
  if (current) return <Canvas project={name} flowName={current} canEdit={canEdit} noRepo={list.data?.why} onClose={() => { setParams({}); list.reload(); }} />;
  if (list.error && !list.data) return <div className="page"><ErrorBanner error={list.error} /></div>;
  if (!list.data) return <div className="page"><Spinner /></div>;
  const flows = list.data.flows;
  return (
    <div className="page stack" data-testid="flows">
      <p className="lead" style={{ margin: 0 }}>
        Pipelines built on a canvas: each step a dbt model in the project's repo, previewed on a sample as you, saved as a merge request.
      </p>
      {list.data.why && <div className="banner" data-testid="flows-why">Flows can be built and previewed here; saving one needs the project's pipelines repo ({list.data.why}).</div>}
      {canEdit && (
        <form className="row" onSubmit={(e) => { e.preventDefault(); if (/^[a-z][a-z0-9_]{0,40}$/.test(newName)) setParams({ flow: newName, new: "1" }); }}>
          <input placeholder="a new flow's name (weekly_revenue)" value={newName} onChange={(e) => setNewName(e.target.value)} data-testid="new-flow-name" />
          <button className="btn primary" type="submit" disabled={!/^[a-z][a-z0-9_]{0,40}$/.test(newName)} data-testid="new-flow">New flow</button>
          <span className="small muted">{newName && !/^[a-z][a-z0-9_]{0,40}$/.test(newName) ? "A flow's name is a dbt model's: lowercase letters, digits and underscores, starting with a letter (weekly_revenue)." : "The name becomes the dbt model, and the table it makes."}</span>
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
      <Uploads project={name} canEdit={canEdit} />
    </div>
  );
}

// Uploads are the files in the project's landing area (landing/uploads/ in
// its bucket), put there as you; a CSV is previewed with the types the
// platform reads. Importing one makes it a table (the import "uploads",
// run by the project's pipelines), which a flow takes as an input.
function Uploads({ project, canEdit }: { project: string; canEdit: boolean }) {
  const { view, reload } = useProject();
  const list = usePoll(() => getUploads(project), [project]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [shown, setShown] = useState<{ name: string; preview: UploadPreview; table: string }>();
  const imports = view?.spec?.spec.imports ?? [];
  const uploads = imports.find((i) => i.name === "uploads" && i.source === "files");
  const tableOf = (file: string) => uploads?.files?.tables.find((t) => t.file === file)?.name;
  const act = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try { await f(); list.reload(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  const upload = (file: File) => act(async () => { const r = await uploadFile(project, file); setShown({ name: r.upload.name, ...r }); });
  const importAs = (file: string, table: string) => act(async () => {
    const tables = [...(uploads?.files?.tables ?? []).filter((t) => t.file !== file), { name: table, file }];
    await putImport(project, { name: "uploads", source: "files", schedule: uploads?.schedule ?? "0 * * * *", files: { tables } });
    reload();
  });
  return (
    <div className="card stack" style={{ gap: 8 }} data-testid="uploads">
      <div className="row">
        <strong>Uploads</strong>
        <span className="small muted">Files in the project's landing area; a CSV or Excel file imported becomes a table a flow can read.</span>
        <span className="grow" />
        {canEdit && <label className="btn small">{busy ? "…" : "Upload a file"}
          <input type="file" accept=".csv,.xlsx,.xls,.jsonl,.parquet" style={{ display: "none" }} disabled={busy} data-testid="upload-file"
            onChange={(e) => { const f = e.target.files?.[0]; if (f) upload(f); e.target.value = ""; }} />
        </label>}
      </div>
      <ErrorBanner error={error ?? list.error} />
      {list.data?.uploads.length === 0 && <p className="small muted" style={{ margin: 0 }}>No uploads yet.</p>}
      {list.data?.uploads.map((u: Upload) => (
        <div key={u.name} className="row small" data-testid={`upload-${u.name}`}>
          <button className="linkish mono" onClick={() => act(async () => setShown(await getUploadPreview(project, u.name)))}>{u.name}</button>
          <span className="muted">{(u.size / 1024).toFixed(1)} KB</span>
          {tableOf(u.name) && <span className="pill">table uploads_{tableOf(u.name)}</span>}
          <span className="grow" />
          {canEdit && u.format && !tableOf(u.name) && <button className="btn small" disabled={busy} data-testid={`import-${u.name}`}
            onClick={() => { const t = prompt("The table's name (uploads_<name>):", (shown?.name === u.name && shown.table) || u.name.replace(/\.[^.]+$/, "").toLowerCase().replace(/[^a-z0-9_]+/g, "_")); if (t) importAs(u.name, t); }}>Import as a table</button>}
          {canEdit && <button className="btn small" disabled={busy} onClick={() => { if (confirm(`Remove ${u.name}?`)) act(() => removeUpload(project, u.name)); }}>Remove</button>}
        </div>
      ))}
      {shown && (
        <div data-testid="upload-preview">
          <p className="small muted" style={{ margin: "4px 0" }}>{shown.name}: {shown.preview.note ?? `${shown.preview.columns.length} columns, as the platform reads them${shown.preview.partial ? " (the first part of the file)" : ""}.`}</p>
          {shown.preview.columns.length > 0 && (
            <div className="scroll-x"><table className="t data">
              <thead><tr>{shown.preview.columns.map((c) => <th key={c.name}>{c.name} <span className="muted">{c.type}</span></th>)}</tr></thead>
              <tbody>{shown.preview.rows.map((r, i) => <tr key={i}>{r.map((v, j) => <td key={j}>{v}</td>)}</tr>)}</tbody>
            </table></div>
          )}
        </div>
      )}
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

function Canvas({ project, flowName, canEdit, noRepo, onClose }: { project: string; flowName: string; canEdit: boolean; noRepo?: string; onClose: () => void }) {
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
  const [tables, setTables] = useState<FlowTable[]>();
  const [metrics, setMetrics] = useState<SemanticMetric[]>();
  const timer = useRef<number>();

  useEffect(() => { // for the step settings' dropdowns: the project's tables (and the products it reads), and its metrics
    getFlow(project).then((f) => setTables(f.tables)).catch(() => setTables([]));
    getSemantic(project).then((m) => setMetrics(m.metrics ?? [])).catch(() => setMetrics([]));
  }, [project]);

  useEffect(() => {
    if (params.get("new") || noRepo) {
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
    const step: FlowStep = { id, kind, x: from ? from.x + 190 : 30 + (steps.length % 5) * 190, y: from ? from.y : 30 + Math.floor(steps.length / 5) * 90 };
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
        {canEdit && <button className="btn primary" onClick={save} disabled={saving || errors.length > 0 || steps.length === 0 || !!noRepo} data-testid="save-flow"
          title={noRepo ? `Saving needs the project's pipelines repo: ${noRepo}` : undefined}>{saving ? "Saving…" : "Save as merge request"}</button>}
      </div>
      <ErrorBanner error={saveError} />
      {errors.length > 0 && <div className="banner error" data-testid="flow-errors">{errors.map((e) => <div key={e}>{e}</div>)}</div>}
      <div className="split">
        <div className="main canvas" style={{ flex: "999 1 480px" }}>
          <ReactFlow nodes={nodes} edges={edges} nodeTypes={nodeTypes} onNodesChange={onNodesChange} onEdgesChange={onEdgesChange} onConnect={onConnect}
            nodesDraggable={canEdit} nodesConnectable={canEdit} elementsSelectable fitView fitViewOptions={{ padding: 0.2, maxZoom: 1.1 }} minZoom={0.25}
            deleteKeyCode={canEdit ? ["Backspace", "Delete"] : null} proOptions={{ hideAttribution: true }}>
            <Background />
            <Controls showInteractive={false} />
          </ReactFlow>
        </div>
        <div className="flowside card" data-testid="step-settings">
          {!step && <p className="muted small" style={{ margin: 0 }}>{steps.length === 0 ? "Add an input to start." : "Pick a step to see its settings."}</p>}
          {step && (
            <Settings key={step.id} step={step} steps={steps} inputs={inputColumns(step)} columns={result?.columns ?? undefined} canEdit={canEdit} update={update}
              tables={tables} metrics={metrics} remove={() => { remove(step.id); setSelected(undefined); }} />
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

type SettingsProps = {
  step: FlowStep; steps: FlowStep[]; inputs: StepColumn[][]; columns?: StepColumn[]; canEdit: boolean; update: (s: FlowStep) => void; remove: () => void;
  tables?: FlowTable[]; metrics?: SemanticMetric[];
};

const list = (v?: string[]) => (v ?? []).join(", ");
const split = (v: string) => v.split(",").map((x) => x.trim()).filter(Boolean);
// tableKey names a table as the input step's dropdown lists it: the project's own by name, another's product as namespace.table.
const tableKey = (t: { table?: string; namespace?: string } | FlowTable) =>
  "fqn" in t ? (t.external ? `${t.fqn.split(".")[1]}.${t.name}` : t.name) : t.namespace ? `${t.namespace}.${t.table ?? ""}` : t.table ?? "";

function Settings({ step, steps, inputs, canEdit, update, remove, tables, metrics }: SettingsProps) {
  const set = (patch: Partial<FlowStep>) => update({ ...step, ...patch });
  const ro = !canEdit;
  const cols = inputs[0] ?? [];
  const names = cols.map((c) => c.name);
  // A pick from a list, with what's set but not listed kept as a choice (an input whose columns aren't known yet).
  const Pick = ({ value, onChange, allow, what, testid }: { value?: string; onChange: (v: string) => void; allow: string[]; what: string; testid?: string }) => (
    <select value={value ?? ""} onChange={(e) => onChange(e.target.value)} disabled={ro} data-testid={testid}>
      <option value="">{what}…</option>
      {value && !allow.includes(value) && <option value={value}>{value}</option>}
      {allow.map((n) => <option key={n} value={n}>{n}</option>)}
    </select>
  );
  const ColumnPick = ({ value, onChange, allow = names }: { value?: string; onChange: (v: string) => void; allow?: string[] }) => (
    <Pick value={value} onChange={onChange} allow={allow} what="column" />
  );
  // Columns, several: a dropdown each, in order.
  const ColumnList = ({ value, onChange, allow = names, add, testid }: { value?: string[]; onChange: (v: string[]) => void; allow?: string[]; add: string; testid?: string }) => (
    <div className="rows" data-testid={testid}>
      {(value ?? []).map((v, i) => (
        <div key={i} className="row">
          <ColumnPick value={v} allow={allow} onChange={(n) => onChange((value ?? []).map((x, j) => (j === i ? n : x)))} />
          {!ro && <button type="button" className="btn small" onClick={() => onChange((value ?? []).filter((_, j) => j !== i))} aria-label="Remove">×</button>}
        </div>
      ))}
      {!ro && <button type="button" className="btn small" onClick={() => onChange([...(value ?? []), ""])}>{add}</button>}
    </div>
  );
  const TypePick = ({ value, onChange, style }: { value?: string; onChange: (v: string) => void; style?: React.CSSProperties }) => (
    <><input value={value ?? ""} readOnly={ro} placeholder="type" list={`types-${step.id}`} onChange={(e) => onChange(e.target.value)} style={style} />
      <datalist id={`types-${step.id}`}>{TYPES.map((t) => <option key={t} value={t} />)}</datalist></>
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
      {inputsOf(step.kind) === 1 && (
        <label>Input<Pick value={step.inputs?.[0]} allow={others} what="a step" testid="inputs" onChange={(v) => set({ inputs: v ? [v] : [] })} /></label>
      )}
      {step.kind === "join" && (
        <label>Inputs (left, right)
          <div className="row" style={{ gap: 4 }}>
            <Pick value={step.inputs?.[0]} allow={others} what="left" testid="inputs" onChange={(v) => set({ inputs: [v, step.inputs?.[1] ?? ""].filter((x, i) => x || i === 0) })} />
            <Pick value={step.inputs?.[1]} allow={others} what="right" onChange={(v) => set({ inputs: [step.inputs?.[0] ?? "", v] })} />
          </div>
        </label>
      )}
      {inputsOf(step.kind) > 2 && (
        <label>Inputs<ColumnList value={step.inputs} allow={others} add="Add an input" testid="inputs" onChange={(inputs) => set({ inputs })} /></label>
      )}
      <label>Description<input value={step.description ?? ""} readOnly={ro} onChange={(e) => set({ description: e.target.value })} /></label>
      {step.kind === "input" && <>
        <label>Table
          <select value={tableKey(step)} disabled={ro} data-testid="input-table" onChange={(e) => {
            const t = (tables ?? []).find((x) => tableKey(x) === e.target.value);
            if (t) set({ table: t.name, namespace: t.external ? t.fqn.split(".")[1] : undefined });
            else set({ table: e.target.value === "other" ? step.table ?? "" : e.target.value, namespace: step.namespace });
          }}>
            <option value="">table…</option>
            {step.table && !(tables ?? []).some((t) => tableKey(t) === tableKey(step)) && <option value={tableKey(step)}>{tableKey(step)}</option>}
            {(tables ?? []).filter((t) => !t.external).map((t) => <option key={t.fqn} value={tableKey(t)}>{t.name}</option>)}
            {(tables ?? []).some((t) => t.external) && <optgroup label="Products of other projects">
              {(tables ?? []).filter((t) => t.external).map((t) => <option key={t.fqn} value={tableKey(t)}>{tableKey(t)}</option>)}
            </optgroup>}
            <option value="other">another table…</option>
          </select>
        </label>
        {(tableKey(step) === "" || !(tables ?? []).some((t) => tableKey(t) === tableKey(step))) && <>
          <label>Table name<input value={step.table ?? ""} readOnly={ro} onChange={(e) => set({ table: e.target.value })} placeholder="orders" data-testid="input-table-name" /></label>
          <label>Namespace (another project's, for a product it shares)<input value={step.namespace ?? ""} readOnly={ro} onChange={(e) => set({ namespace: e.target.value || undefined })} placeholder="the project's own" /></label>
        </>}
      </>}
      {step.kind === "select" && <>
        <span className="small muted">Kept columns, in order, each with its new name if renamed.</span>
        <Rows items={step.columns ?? []} blank={{ name: "" }} onChange={(columns) => set({ columns })} add="Keep a column"
          render={(c, s) => <><ColumnPick value={c.name} onChange={(name) => s({ ...c, name })} /><input value={c.as ?? ""} readOnly={ro} placeholder="as" onChange={(e) => s({ ...c, as: e.target.value || undefined })} /></>} />
        {!ro && <button type="button" className="btn small" onClick={() => set({ columns: names.map((name) => ({ name })) })}>Keep all</button>}
      </>}
      {step.kind === "retype" && (
        <Rows items={step.columns ?? []} blank={{ name: "", type: "varchar" }} onChange={(columns) => set({ columns })} add="Cast a column"
          render={(c, s) => <><ColumnPick value={c.name} onChange={(name) => s({ ...c, name })} /><TypePick value={c.type} onChange={(type) => s({ ...c, type })} /></>} />
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
        <label>Same when these columns are equal (none: whole rows)<ColumnList value={step.by} add="Add a column" onChange={(by) => set({ by })} /></label>
        <span className="small muted">Keep the first by:</span>
        <Rows items={step.order ?? []} blank={{ column: "" }} onChange={(order) => set({ order })} add="Order by"
          render={(o, s) => <><ColumnPick value={o.column} onChange={(column) => s({ ...o, column })} /><label className="row" style={{ flexDirection: "row", margin: 0 }}><input type="checkbox" checked={!!o.desc} disabled={ro} onChange={(e) => s({ ...o, desc: e.target.checked })} />desc</label></>} />
      </>}
      {step.kind === "group" && <>
        <label>Group by<ColumnList value={step.by} add="Add a column" testid="group-by" onChange={(by) => set({ by })} /></label>
        <Rows items={step.aggregates ?? []} blank={{ function: "sum", column: "" }} onChange={(aggregates) => set({ aggregates })} add="Add an aggregate"
          render={(a, s) => <>
            <select value={a.function} disabled={ro} onChange={(e) => s({ ...a, function: e.target.value })}>{AGGREGATES.map((f) => <option key={f}>{f}</option>)}</select>
            <ColumnPick value={a.column} onChange={(column) => s({ ...a, column })} />
            <input value={a.as ?? ""} readOnly={ro} placeholder="as" onChange={(e) => s({ ...a, as: e.target.value || undefined })} />
          </>} />
      </>}
      {step.kind === "pivot" && <>
        <label>Rows (group by)<ColumnList value={step.by} add="Add a column" onChange={(by) => set({ by })} /></label>
        <label>Column whose values become columns<ColumnPick value={step.column} onChange={(column) => set({ column })} /></label>
        <label>Its values<input value={list(step.values)} readOnly={ro} onChange={(e) => set({ values: split(e.target.value) })} placeholder="EU, US" /></label>
        <label>Value<ColumnPick value={step.value} onChange={(value) => set({ value })} /></label>
        <label>Aggregate<select value={step.function ?? "sum"} disabled={ro} onChange={(e) => set({ function: e.target.value })}>{["sum", "count", "min", "max", "avg"].map((f) => <option key={f}>{f}</option>)}</select></label>
      </>}
      {step.kind === "unpivot" && <>
        <label>Columns that become rows<ColumnList value={step.unpivot} add="Add a column" onChange={(unpivot) => set({ unpivot })} /></label>
        <label>Name column<input value={step.name ?? ""} readOnly={ro} onChange={(e) => set({ name: e.target.value || undefined })} placeholder="name" /></label>
        <label>Value column<input value={step.value ?? ""} readOnly={ro} onChange={(e) => set({ value: e.target.value || undefined })} placeholder="value" /></label>
      </>}
      {step.kind === "window" && <>
        <label>Partition by<ColumnList value={step.partition} add="Add a column" onChange={(partition) => set({ partition })} /></label>
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
            <TypePick value={c.type} onChange={(type) => s({ ...c, type: type || undefined })} style={{ flex: "0 1 90px" }} />
          </>} />
      </>}
      {step.kind === "metric" && <>
        <span className="small muted">A metric query on the project's definitions (and the company's it builds on): its SQL is the semantic layer's.</span>
        <label>Metrics<ColumnList value={step.metrics} allow={(metrics ?? []).map((m) => m.name)} add="Add a metric" testid="metrics" onChange={(ms) => set({ metrics: ms })} /></label>
        <label>Group by<ColumnList value={step.by} add="Add a dimension" onChange={(by) => set({ by })}
          allow={[...new Set((metrics ?? []).filter((m) => (step.metrics ?? []).includes(m.name)).flatMap((m) => m.dimensions))].sort()} /></label>
        <label>Where<input value={step.where ?? ""} readOnly={ro} onChange={(e) => set({ where: e.target.value || undefined })} placeholder="{{ Dimension('order__region') }} = 'EU'" /></label>
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
