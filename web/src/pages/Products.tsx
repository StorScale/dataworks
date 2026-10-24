import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { answerRequest, askForProduct, certify, deleteProduct, getFlow, getProducts, getTerms, listProjects, putProduct, removeConsumer, type Product } from "../api";
import { Empty } from "../components";
import { ErrorBanner, Spinner, usePlatform, usePoll } from "../lib";

// Data products (use case 10.3): tables projects publish for others, with an
// owner, a version and a contract; read in place by the projects their
// producers approve, under the producer's rules; certified by the governance
// group, certified ones first.
export default function Products() {
  const { data, error, reload } = usePoll(() => getProducts(), [], 30_000);
  const { data: mine } = usePoll(() => listProjects(), [], 120_000);
  const [params] = useSearchParams();
  const [actionError, setActionError] = useState<string>();
  const [editing, setEditing] = useState<Product | "new">();
  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  const edits = (mine?.projects ?? []).filter((p) => p.role === "editor").map((p) => p.name);
  // Who may publish: those who may write a project's tables (editors, and roles giving write-tables).
  const publishes = (mine?.projects ?? []).filter((p) => p.role === "editor" || (p.can ?? []).includes("write-tables")).map((p) => p.name);
  const focus = params.get("product");
  const act = async (f: () => Promise<unknown>) => { setActionError(undefined); try { await f(); reload(); } catch (e) { setActionError((e as Error).message); } };
  return (
    <div className="page stack" data-testid="products">
      <div className="page-head">
      <div>
      <h1>Data products</h1>
      <p className="lead">
        Tables a project publishes for others, with an owner, a version and a promise (its contract). Ask for one for a project you edit;
        once its producers approve, your project reads it in place, under their rules: no copy. Certified ones the governance group has checked.
      </p>
      </div>
      {publishes.length > 0 && !editing && <button className="btn primary" onClick={() => setEditing("new")} data-testid="publish-product">Publish a data product</button>}
      </div>
      <ErrorBanner error={actionError} />
      {editing && <ProductForm projects={publishes} product={editing === "new" ? undefined : editing} onDone={() => { setEditing(undefined); reload(); }} />}
      {data.products.length === 0 && !editing && <Empty>No data products yet.{publishes.length > 0 ? " Publish one of your project's tables." : ""}</Empty>}
      {data.products.map((p) => (
        <ProductCard key={p.project + "/" + p.name} p={p} open={focus === p.project + "/" + p.name} edits={edits.filter((e) => e !== p.project)}
          canCertify={data.canCertify} act={act} canPublish={publishes.includes(p.project)} onChange={() => setEditing(p)} />
      ))}
    </div>
  );
}

function ProductCard({ p, open, edits, canCertify, act, canPublish, onChange }: {
  p: Product; open: boolean; edits: string[]; canCertify: boolean; act: (f: () => Promise<unknown>) => void; canPublish: boolean; onChange: () => void;
}) {
  const [asking, setAsking] = useState(false);
  const [consumer, setConsumer] = useState(edits[0] ?? "");
  const [why, setWhy] = useState("");
  const asked = edits.filter((e) => !(p.consumers ?? []).includes(e));
  return (
    <div className="card stack" style={{ gap: 8, outline: open ? "2px solid var(--accent)" : undefined }} data-testid={`product-${p.project}-${p.name}`}>
      <div className="card-head">
        <h2><span className="mono">{p.name}</span><span className="small muted" style={{ fontWeight: 400 }}>from <Link to={`/p/${p.project}`}>{p.project}</Link></span>
          <span className={`pill ${p.certified ? "green" : ""}`}>{p.certified ? "certified" : "not certified"}</span>
          <span className="pill">version {p.version}</span></h2>
        <div className="row">
        {asked.length > 0 && !asking && <button className="btn small" onClick={() => { setConsumer(asked[0]); setAsking(true); }} data-testid={`ask-${p.name}`}>Ask for it</button>}
        {canCertify && <button className="btn small" onClick={() => act(() => certify(p, !p.certified))}>{p.certified ? "Take certification away" : "Certify"}</button>}
        {canPublish && <button className="btn small" onClick={onChange} data-testid={`change-${p.name}`}>Change</button>}
        {canPublish && <button className="btn small danger" disabled={(p.consumers ?? []).length > 0}
          title={(p.consumers ?? []).length ? `${(p.consumers ?? []).join(", ")} read it: remove them first` : undefined}
          onClick={() => { if (confirm(`Take ${p.name} back? No other project reads it.`)) act(() => deleteProduct(p.project, p.name)); }}>Remove</button>}
        </div>
      </div>
      {p.description && <div>{p.description}</div>}
      <div className="small">
        Table <span className="mono">{p.table}</span>, owned by {p.owner}.
        {p.contract && <> Promises: {[
          p.contract.columns?.length ? <>columns <span className="mono">{p.contract.columns.join(", ")}</span></> : null,
          p.contract.within ? <>changed within {p.contract.within}</> : null,
          p.contract.checks ? <>its checks passing</> : null,
        ].filter(Boolean).map((x, i) => <span key={i}>{i ? "; " : ""}{x}</span>)}.</>}
        {p.terms?.length ? <> Holds {p.terms.map((t, i) => <span key={t}>{i ? ", " : ""}<Link to={`/terms/${encodeURIComponent(t)}`}>{t}</Link></span>)}.</> : null}
      </div>
      <div className="small muted">
        {p.certification && p.certified ? `Certified by ${p.certification.by} ${new Date(p.certification.at).toLocaleDateString()}. ` : ""}
        Read by: {(p.consumers ?? []).length ? (p.consumers ?? []).join(", ") : "no other project yet"}.
      </div>
      {p.canApprove && (p.consumers ?? []).length > 0 && (
        <div className="row small" style={{ flexWrap: "wrap", gap: 8 }}>
          {(p.consumers ?? []).map((c) => <button key={c} className="btn small" onClick={() => { if (confirm(`Stop ${c} reading ${p.name}?`)) act(() => removeConsumer(p, c)); }}>Remove {c}</button>)}
        </div>
      )}
      {p.canApprove && p.requests.length > 0 && (
        <div className="stack" style={{ gap: 6 }} data-testid={`requests-${p.name}`}>
          <span className="field-label">Asked for</span>
          {p.requests.map((q) => (
            <div key={q.consumer} className="row small">
              <span><span className="mono">{q.consumer}</span>, by {q.by}{q.why ? `: “${q.why}”` : ""}</span>
              <span className="grow" />
              <button className="btn small primary" onClick={() => act(() => answerRequest(p, q.consumer, true))}>Approve</button>
              <button className="btn small" onClick={() => act(() => answerRequest(p, q.consumer, false))}>Decline</button>
            </div>
          ))}
        </div>
      )}
      {asking && (
        <form className="subcard stack" onSubmit={(e) => { e.preventDefault(); act(() => askForProduct(p, consumer, why)); setAsking(false); }}>
          <div className="fields">
            <label>For the project<select value={consumer} onChange={(e) => setConsumer(e.target.value)}>{asked.map((e) => <option key={e}>{e}</option>)}</select></label>
            <label className="grow">What it's for<input value={why} onChange={(e) => setWhy(e.target.value)} placeholder="The regional forecast" /></label>
          </div>
          <div className="form-actions">
            <button className="btn primary">Ask</button>
            <button type="button" className="btn" onClick={() => setAsking(false)}>Cancel</button>
          </div>
        </form>
      )}
    </div>
  );
}

// ProductForm publishes one of a project's tables as a data product, or
// changes its promise: the table, what it is, who answers for it, its
// version, the business terms it holds, and its contract (the columns kept,
// how fresh it stays, its checks passing). A changed promise lapses a
// certification; who reads it stays.
function ProductForm({ projects, product, onDone }: { projects: string[]; product?: Product; onDone: () => void }) {
  const { user } = usePlatform();
  const [project, setProject] = useState(product?.project ?? projects[0] ?? "");
  const [name, setName] = useState(product?.name ?? "");
  const [table, setTable] = useState(product?.table ?? "");
  const [description, setDescription] = useState(product?.description ?? "");
  const [owner, setOwner] = useState(product?.owner ?? user.username);
  const [version, setVersion] = useState(product?.version ?? "1");
  const [terms, setTerms] = useState<string[]>(product?.terms ?? []);
  const [columns, setColumns] = useState<string[]>(product?.contract?.columns ?? []);
  const [within, setWithin] = useState(product?.contract?.within ?? "24h");
  const [checks, setChecks] = useState(product?.contract?.checks ?? false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const flow = usePoll(() => (project ? getFlow(project) : Promise.resolve(undefined)), [project], 300_000);
  const termList = usePoll(getTerms, [], 300_000);
  const tables = (flow.data?.tables ?? []).filter((t) => !t.external);
  const picked = tables.find((t) => t.name === table);
  const toggle = (list: string[], set: (x: string[]) => void, x: string) => set(list.includes(x) ? list.filter((y) => y !== x) : [...list, x]);
  const save = async () => {
    setBusy(true); setError(undefined);
    try {
      await putProduct(project, name, { table, description: description || undefined, owner, version, terms: terms.length ? terms : undefined,
        contract: columns.length || within || checks ? { columns: columns.length ? columns : undefined, within: within || undefined, checks: checks || undefined } : undefined });
      onDone();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <form className="card stack" onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="product-form">
      <h2>{product ? `Change ${product.name}` : "Publish a data product"}</h2>
      <ErrorBanner error={error} />
      <div className="fields">
        <label>From the project<select value={project} disabled={!!product} onChange={(e) => { setProject(e.target.value); setTable(""); setColumns([]); }} data-testid="product-project">
          {projects.map((p) => <option key={p}>{p}</option>)}</select></label>
        <label>Its table{tables.length ? (
          <select value={table} onChange={(e) => { setTable(e.target.value); setColumns([]); if (!name) setName(e.target.value.replace(/^uploads_/, "")); }} data-testid="product-table">
            <option value="">Pick one</option>
            {tables.map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
          </select>) : <input className="mono" value={table} onChange={(e) => setTable(e.target.value)} placeholder="uploads_orders" data-testid="product-table" />}</label>
        <label>Name<input className="mono" value={name} disabled={!!product} onChange={(e) => setName(e.target.value)} placeholder="orders" data-testid="product-name" /></label>
        <label>Version<input className="mono" value={version} onChange={(e) => setVersion(e.target.value)} style={{ width: 80 }} data-testid="product-version" /></label>
      </div>
      <div className="fields">
        <label className="grow">What it is<input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Harbor Outfitters' orders, net of test rows" data-testid="product-description" /></label>
        <label>Who answers for it<input className="mono" value={owner} onChange={(e) => setOwner(e.target.value)} data-testid="product-owner" /></label>
      </div>
      {(termList.data?.terms ?? []).length > 0 && (
        <div className="stack" style={{ gap: 6 }}>
          <span className="field-label">The business terms it holds</span>
          <div className="row" style={{ flexWrap: "wrap", gap: 12 }}>
            {termList.data!.terms.map((t) => (
              <label key={t.name} className="check"><input type="checkbox" checked={terms.includes(t.name)} onChange={() => toggle(terms, setTerms, t.name)} data-testid={`product-term-${t.name}`} />
                <span>{t.label || t.name} <span className="muted small">({t.project})</span></span></label>
            ))}
          </div>
        </div>
      )}
      <div className="stack" style={{ gap: 6 }}>
        <span className="field-label">What it promises its consumers</span>
        {picked ? (
          <div className="row" style={{ flexWrap: "wrap", gap: 12 }}>
            {picked.columns.map((c) => (
              <label key={c.name} className="check"><input type="checkbox" checked={columns.includes(c.name)} onChange={() => toggle(columns, setColumns, c.name)} data-testid={`product-column-${c.name}`} />
                <span className="mono small">{c.name}</span></label>
            ))}
          </div>
        ) : (
          <label>Columns kept<input className="mono" value={columns.join(", ")} onChange={(e) => setColumns(e.target.value.split(",").map((x) => x.trim()).filter(Boolean))}
            placeholder="order_id, customer_id, amount" data-testid="product-columns" /></label>
        )}
        <span className="field-help">The columns it keeps, with these names: renaming or dropping one breaks the promise, and its consumers are named.</span>
        <div className="fields">
          <label>Changed at least every<input className="mono" value={within} onChange={(e) => setWithin(e.target.value)} placeholder="24h" style={{ width: 90 }} data-testid="product-within" /></label>
          <label className="check" style={{ alignSelf: "end" }}><input type="checkbox" checked={checks} onChange={(e) => setChecks(e.target.checked)} data-testid="product-checks" />
            <span>The project's checks on its table pass</span></label>
        </div>
      </div>
      <div className="form-actions">
        <button className="btn primary" type="submit" disabled={busy || !project || !name || !table || !owner || !version} data-testid="save-product">
          {busy ? "Saving…" : product ? "Save" : "Publish it"}</button>
        <button className="btn" type="button" onClick={onDone}>Cancel</button>
        <span className="field-help">{product?.certified ? "A changed promise lapses its certification: the governance group certifies it again." : "Other projects ask for it here; your project's editors approve."}</span>
      </div>
    </form>
  );
}
