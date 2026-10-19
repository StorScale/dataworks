import { Link, useParams } from "react-router-dom";
import { getTerms, type Term } from "../api";
import { ErrorBanner, Spinner, usePoll } from "../lib";

const kinds: Record<string, string> = { entity: "Entity", metric: "Metric", dimension: "Dimension", "": "Term" };

// The company's terms (use case 10.1): what Customer, Revenue and the rest
// mean, who owns each, where it comes from and who uses it, from the shared
// semantic model, before any table.
export default function Terms() {
  const { term } = useParams();
  const { data, error } = usePoll(() => getTerms(), [], 60_000);
  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  const t = term ? data.terms.find((x) => x.name === term) : undefined;
  if (term && !t) return <div className="page"><p className="muted">No term “{term}”. <Link to="/terms">All terms</Link></p></div>;
  return t ? <TermPage t={t} all={data.terms} /> : (
    <div className="page stack" data-testid="terms">
      <h1 style={{ margin: 0 }}>Business terms</h1>
      <p className="lead" style={{ margin: 0 }}>
        The company's words for its data, defined once in the shared semantic model: each project's metrics build on them,
        and dashboards, SQL and assistants give the same answers.
      </p>
      {data.terms.length === 0 && <p className="muted">No terms yet: a shared project's semantic model defines them (semantic.shared, and its terms).</p>}
      {data.terms.length > 0 && (
        <table className="t">
          <thead><tr><th>Term</th><th>What it is</th><th>Owner</th><th>Used by</th></tr></thead>
          <tbody>
            {data.terms.map((x) => (
              <tr key={x.name}>
                <td><Link to={`/terms/${encodeURIComponent(x.name)}`} data-testid={`term-${x.name}`}><strong>{x.label}</strong></Link>
                  <div className="small muted">{kinds[x.kind]}</div></td>
                <td className="small">{x.definition || <span className="muted">No definition yet.</span>}</td>
                <td className="small">{x.owner || "–"}</td>
                <td className="small">{x.usedBy.length ? x.usedBy.join(", ") : "–"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function TermPage({ t, all }: { t: Term; all: Term[] }) {
  const related = all.filter((x) => x.name !== t.name && t.metrics.includes(x.name));
  return (
    <div className="page stack" data-testid={`term-page-${t.name}`}>
      <div className="small"><Link to="/terms">Business terms</Link> ›</div>
      <div className="row"><h1 style={{ margin: 0 }}>{t.label}</h1><span className="pill">{kinds[t.kind]}</span></div>
      <p className="lead" style={{ margin: 0 }}>{t.definition || <span className="muted">No definition yet.</span>}</p>
      <div className="card stack small" style={{ gap: 6 }}>
        <div>Owner: <strong>{t.owner || "–"}</strong></div>
        {t.synonyms.length > 0 && <div>Also called: {t.synonyms.join(", ")}</div>}
        <div>Defined in the shared model of <Link to={`/p/${t.project}/metrics`}>{t.project}</Link>, as <span className="mono">{t.name}</span>.</div>
      </div>
      {t.metrics.length > 0 && (
        <div className="card stack" style={{ gap: 6 }}>
          <strong>Metrics on it</strong>
          <div className="small">{t.metrics.map((m) => {
            const term = related.find((x) => x.name === m);
            return <span key={m} style={{ marginRight: 12 }}>{term ? <Link to={`/terms/${encodeURIComponent(m)}`}>{term.label}</Link> : <span className="mono">{m}</span>}</span>;
          })}</div>
        </div>
      )}
      <div className="card stack" style={{ gap: 6 }}>
        <strong>Where it comes from</strong>
        <div className="small mono">{t.tables.length ? t.tables.join(", ") : "–"}</div>
      </div>
      <div className="card stack" style={{ gap: 6 }}>
        <strong>Projects using it</strong>
        <div className="small">{t.usedBy.length ? t.usedBy.map((p) => <Link key={p} to={`/p/${p}/metrics`} style={{ marginRight: 12 }}>{p}</Link>) : "None yet."}</div>
      </div>
    </div>
  );
}
