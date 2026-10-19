import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { answerRequest, askForProduct, certify, getProducts, listProjects, removeConsumer, type Product } from "../api";
import { ErrorBanner, Spinner, usePoll } from "../lib";

// Data products (use case 10.3): tables projects publish for others, with an
// owner, a version and a contract; read in place by the projects their
// producers approve, under the producer's rules; certified by the governance
// group, certified ones first.
export default function Products() {
  const { data, error, reload } = usePoll(() => getProducts(), [], 30_000);
  const { data: mine } = usePoll(() => listProjects(), [], 120_000);
  const [params] = useSearchParams();
  const [actionError, setActionError] = useState<string>();
  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  const edits = (mine?.projects ?? []).filter((p) => p.role === "editor").map((p) => p.name);
  const focus = params.get("product");
  const act = async (f: () => Promise<unknown>) => { setActionError(undefined); try { await f(); reload(); } catch (e) { setActionError((e as Error).message); } };
  return (
    <div className="page stack" data-testid="products">
      <h1 style={{ margin: 0 }}>Data products</h1>
      <p className="lead" style={{ margin: 0 }}>
        Tables a project publishes for others, with an owner, a version and a promise (its contract). Ask for one for a project you edit;
        once its producers approve, your project reads it in place, under their rules: no copy. Certified ones the governance group has checked.
      </p>
      <ErrorBanner error={actionError} />
      {data.products.length === 0 && <p className="muted">No data products yet: a project publishes one in its spec (products).</p>}
      {data.products.map((p) => (
        <ProductCard key={p.project + "/" + p.name} p={p} open={focus === p.project + "/" + p.name} edits={edits.filter((e) => e !== p.project)}
          canCertify={data.canCertify} act={act} />
      ))}
    </div>
  );
}

function ProductCard({ p, open, edits, canCertify, act }: {
  p: Product; open: boolean; edits: string[]; canCertify: boolean; act: (f: () => Promise<unknown>) => void;
}) {
  const [asking, setAsking] = useState(false);
  const [consumer, setConsumer] = useState(edits[0] ?? "");
  const [why, setWhy] = useState("");
  const asked = edits.filter((e) => !(p.consumers ?? []).includes(e));
  return (
    <div className="card stack" style={{ gap: 8, outline: open ? "2px solid var(--accent)" : undefined }} data-testid={`product-${p.project}-${p.name}`}>
      <div className="row">
        <h3 style={{ margin: 0 }}><span className="mono">{p.name}</span> <span className="small muted">from <Link to={`/p/${p.project}`}>{p.project}</Link></span></h3>
        <span className={`pill ${p.certified ? "green" : ""}`}>{p.certified ? "certified" : "not certified"}</span>
        <span className="pill">version {p.version}</span>
        <span className="grow" />
        {asked.length > 0 && !asking && <button className="btn small" onClick={() => { setConsumer(asked[0]); setAsking(true); }} data-testid={`ask-${p.name}`}>Ask for it</button>}
        {canCertify && <button className="btn small" onClick={() => act(() => certify(p, !p.certified))}>{p.certified ? "Take certification away" : "Certify"}</button>}
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
          <strong className="small">Asked for</strong>
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
        <form className="row small" onSubmit={(e) => { e.preventDefault(); act(() => askForProduct(p, consumer, why)); setAsking(false); }}>
          <label>For <select value={consumer} onChange={(e) => setConsumer(e.target.value)}>{asked.map((e) => <option key={e}>{e}</option>)}</select></label>
          <input placeholder="What it's for" value={why} onChange={(e) => setWhy(e.target.value)} style={{ minWidth: 240 }} />
          <button className="btn primary small">Ask</button>
          <button type="button" className="btn small" onClick={() => setAsking(false)}>Cancel</button>
        </form>
      )}
    </div>
  );
}
