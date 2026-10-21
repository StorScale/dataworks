import { Link } from "react-router-dom";
import { getGovernance } from "../api";
import { ErrorBanner, Spinner, usePoll } from "../lib";

// Governance is every project at once (use case 12.9): what awaits someone's
// approval, the agents and what they spend, the rules readers are under, the
// data products and their certification, and the erasures with their
// deadlines. It changes nothing; each row links to where it's decided.
export default function Governance() {
  const { data, error } = usePoll(getGovernance, [], 30_000);
  return (
    <div className="page stack" data-testid="governance">
      <p className="lead" style={{ margin: 0 }}>
        Across every project: what waits for an approval, the agents and their spend, what readers may see, the data products, and the erasures owed.
      </p>
      <ErrorBanner error={error} />
      {!data ? (!error && <Spinner />) : (
        <>
          <p className="muted" data-testid="governance-summary">{data.summary}.</p>

          <h2>Awaiting approval</h2>
          {data.pending.length === 0 ? <p className="muted">Nothing waits.</p> : (
            <table className="t" data-testid="governance-pending">
              <thead><tr><th>Project</th><th>What</th><th>Waits for</th><th>Since</th></tr></thead>
              <tbody>
                {data.pending.map((p, i) => (
                  <tr key={i}>
                    <td><Link to={`/p/${p.project}`}>{p.project}</Link></td>
                    <td><span className="pill">{p.kind}</span> <Link to={p.link}>{p.name}</Link></td>
                    <td className="small">{p.waits}</td>
                    <td className="small muted">{p.since ? new Date(p.since).toLocaleDateString() : ""}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          <h2>Agents</h2>
          {data.agents.length === 0 ? <p className="muted">No project has an agent.</p> : (
            <table className="t" data-testid="governance-agents">
              <thead><tr><th>Project</th><th>Agent</th><th>Owner</th><th>Tools</th><th>Runs</th><th>Spend</th></tr></thead>
              <tbody>
                {data.agents.map((a) => (
                  <tr key={a.project + "/" + a.name}>
                    <td><Link to={`/p/${a.project}/agents`}>{a.project}</Link></td>
                    <td><strong>{a.name}</strong> {!a.approved && <span className="pill amber">not approved</span>}</td>
                    <td>{a.owner}</td>
                    <td className="small mono">{a.tools.join(", ")}</td>
                    <td className="small">{a.runs ?? <span className="muted">when asked</span>}</td>
                    <td className="small">${a.spend.toFixed(2)}{a.budget > 0 && <span className="muted"> of ${a.budget.toFixed(2)}</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          <h2>What readers see</h2>
          <table className="t" data-testid="governance-rules">
            <thead><tr><th>Project</th><th>Readers</th><th>Only these tables</th><th>Rows</th><th>Masked</th><th>People's data</th></tr></thead>
            <tbody>
              {data.rules.map((r) => (
                <tr key={r.project}>
                  <td><Link to={`/p/${r.project}/access`}>{r.project}</Link></td>
                  <td className="small">{r.readers.join(", ") || <span className="muted">none</span>}</td>
                  <td className="small mono">{r.tables?.join(", ") || <span className="muted">all</span>}</td>
                  <td className="small mono">{r.rowFilters?.map((f) => <div key={f}>{f}</div>) ?? <span className="muted">all</span>}</td>
                  <td className="small mono">{r.masks?.map((m) => <div key={m}>{m}</div>) ?? <span className="muted">none</span>}</td>
                  <td className="small mono">{r.people?.join(", ") ?? <span className="muted">none</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>

          <h2>Data products</h2>
          {data.products.length === 0 ? <p className="muted">No project offers a data product.</p> : (
            <table className="t" data-testid="governance-products">
              <thead><tr><th>Project</th><th>Product</th><th>Owner</th><th>Certified</th><th>Consumers</th></tr></thead>
              <tbody>
                {data.products.map((p) => (
                  <tr key={p.project + "/" + p.name}>
                    <td><Link to={`/p/${p.project}`}>{p.project}</Link></td>
                    <td><Link to="/products">{p.name}</Link></td>
                    <td>{p.owner}</td>
                    <td>{p.certified ? <span className="pill">certified</span> : <span className="pill amber">not yet</span>}</td>
                    <td className="small">{p.consumers.join(", ") || <span className="muted">none</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          <h2>Erasures</h2>
          {data.erasures.length === 0 ? <p className="muted">None recorded, or none you may see.</p> : (
            <table className="t" data-testid="governance-erasures">
              <thead><tr><th>Project</th><th>Recorded</th><th>Due by</th><th>By</th><th>Result</th></tr></thead>
              <tbody>
                {data.erasures.map((e) => (
                  <tr key={e.project + "/" + e.id}>
                    <td><Link to={`/p/${e.project}/deliveries`}>{e.project}</Link></td>
                    <td className="small">{new Date(e.at).toLocaleString()}</td>
                    <td className="small">{e.dueBy}</td>
                    <td>{e.by}</td>
                    <td>{e.errors === 0 ? <span className="pill">erased</span> : <span className="pill red">{e.errors} tables left</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
    </div>
  );
}
