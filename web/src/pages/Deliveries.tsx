import { useState } from "react";
import { approveDelivery, erasePerson, getDeliveries, getErasures, searchDeliveries, type Erasure, type SearchHit } from "../api";
import { ErrorBanner, Spinner, usePlatform, usePoll, useProject } from "../lib";

// Deliveries send files to clients and partners on a schedule (SFTP, S3).
// Each runs only once approved by someone other than who changed what it
// sends (and, with personal columns, a data-protection approver too); its
// runs are the delivery log.
export default function Deliveries() {
  const { name, view } = useProject();
  const { admin } = usePlatform();
  const { data, error, reload } = usePoll(() => getDeliveries(name), [name], 30_000);
  const [busy, setBusy] = useState<string>();
  const [actionError, setActionError] = useState<string>();
  const mayApprove = view?.role === "editor" || admin;
  const approve = async (d: string) => {
    setBusy(d);
    setActionError(undefined);
    try { await approveDelivery(name, d); reload(); } catch (e) { setActionError((e as Error).message); } finally { setBusy(undefined); }
  };
  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  return (
    <div className="page stack" data-testid="deliveries">
      <p className="lead" style={{ margin: 0 }}>
        Files sent to clients and partners on a schedule. A delivery runs once someone other than who changed what it sends has approved it;
        one that sends personal columns needs a data-protection approver too. Each run is kept here: its files, their checksums, where they went.
      </p>
      <ErrorBanner error={actionError} />
      {data.canSearch && <PersonSearch project={name} />}
      {data.canSearch && <Erasures project={name} />}
      {data.deliveries.length === 0 && <p className="muted">No deliveries. The project's spec names them (deliveries).</p>}
      {data.deliveries.map((d) => (
        <div key={d.name} className="card stack" style={{ gap: 8 }} data-testid={`delivery-${d.name}`}>
          <div className="row">
            <h3 style={{ margin: 0 }} className="mono">{d.name}</h3>
            <span className={`pill ${d.approved ? "green" : "amber"}`}>{d.approved ? "approved" : "not approved"}</span>
            <span className="grow" />
            {!d.approved && mayApprove && (
              <button className="btn primary" disabled={busy === d.name} onClick={() => approve(d.name)} data-testid={`approve-${d.name}`}>
                {busy === d.name ? "Approving…" : "Approve"}
              </button>
            )}
          </div>
          <div className="small">
            Sends <span className="mono">{d.table ?? "a query"}</span> as {d.format}{d.gzip ? ", gzipped" : ""}{d.pgp ? ", encrypted (PGP)" : ""}
            {d.splitBy ? <>, a file per <span className="mono">{d.splitBy}</span></> : null} to <span className="mono">{d.target}</span>,
            {" "}on <span className="mono">{d.schedule}</span>, as <span className="mono">{d.files}</span>.
          </div>
          {d.query && <pre className="mono small" style={{ margin: 0, whiteSpace: "pre-wrap" }}>{d.query}</pre>}
          {d.personal.length > 0 && <div className="small">Personal columns: <span className="pill amber">{d.personal.join(", ")}</span></div>}
          <div className="small muted">
            Changed by {d.changedBy || "?"}.
            {d.approval?.by?.length ? ` Approved by ${[...d.approval.by, ...(d.approval.protection ?? [])].join(", ")}.` : ""}
            {d.needs.length > 0 && ` Needs: ${d.needs.join("; ")}.`}
          </div>
          {d.runs.length > 0 && (
            <table className="t">
              <thead><tr><th>Run</th><th>Rows</th><th>Files</th><th>Where</th></tr></thead>
              <tbody>
                {d.runs.map((r) => (
                  <tr key={r.recorded}>
                    <td className="small">{new Date(r.recorded).toLocaleString()}</td>
                    <td className="small">{r.rows}</td>
                    <td className="small">{r.files.map((f) => <div key={f.name} className="mono" title={`sha256 ${f.sha256}`}>{f.name} ({f.bytes} bytes)</div>)}</td>
                    <td className="small mono">{r.target}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      ))}
    </div>
  );
}

// What was sent about a person (data-protection approvers): each run that
// carried their rows, found again from the tables as they were then.
// Erasures are the project's records of people's data deleted (use case 6.9):
// what was deleted where, the deliveries that had carried it, and when the
// tables' old versions stop holding it. Never the person's ID.
function Erasures({ project }: { project: string }) {
  const list = usePoll(() => getErasures(project), [project], 30_000);
  if (!list.data || list.data.erasures.length === 0) return null;
  return (
    <div className="card stack" style={{ gap: 8 }} data-testid="erasures">
      <strong>Erasures</strong>
      <div className="small muted">Each answered within {list.data.deadlineDays} days of the request, as the law sets; the records are kept, without anyone's ID.</div>
      <table className="t">
        <thead><tr><th>When</th><th>Asked on</th><th>Due by</th><th>Deleted</th><th>Deliveries</th><th>By</th><th>Verified by</th></tr></thead>
        <tbody>
          {list.data.erasures.map((e) => (
            <tr key={e.id}>
              <td className="small">{new Date(e.at).toLocaleString()}</td>
              <td className="small">{e.requestedOn}</td>
              <td className="small">{e.dueBy}</td>
              <td className="small">{e.tables.map((t) => `${t.table}: ${t.rows}`).join(", ") || "–"}{e.errors ? " (with errors)" : ""}</td>
              <td className="small">{e.deliveries.length}</td>
              <td className="small">{e.by}</td>
              <td className="small">{e.verifiedBy}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function PersonSearch({ project }: { project: string }) {
  const [person, setPerson] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ person: string; runs: SearchHit[]; checked: number; notKept: number }>();
  const [error, setError] = useState<string>();
  const [erased, setErased] = useState<Erasure>();
  const erase = async () => {
    const requestedOn = prompt("When did the person ask (a date, 2026-10-19)? Leave empty for today.", "") ?? "";
    if (requestedOn === null) return;
    const verifiedBy = prompt("Who verified the request (the privacy tool, or a person)? Leave empty for yourself.", "") ?? "";
    if (!confirm(`Delete every row of ${person.trim()} from this project's tables of people's data? This can't be undone, and is recorded.`)) return;
    setBusy(true);
    setError(undefined);
    try { setErased(await erasePerson(project, person.trim(), requestedOn, verifiedBy)); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  const search = async () => {
    setBusy(true);
    setError(undefined);
    try { setResult({ person, ...(await searchDeliveries(project, person.trim())) }); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <div className="card stack" style={{ gap: 8 }} data-testid="person-search">
      <form className="row" onSubmit={(e) => { e.preventDefault(); if (person.trim()) search(); }}>
        <strong>What was sent about a person</strong>
        <input className="mono" placeholder="Their ID, as C-1042" value={person} onChange={(e) => setPerson(e.target.value)} aria-label="A person's ID" />
        <button className="btn" disabled={busy || !person.trim()}>{busy ? "Searching…" : "Search"}</button>
        <button className="btn" type="button" disabled={busy || !person.trim()} onClick={erase} data-testid="erase">Erase this person</button>
      </form>
      {erased && (
        <div className="banner info" data-testid="erased">
          <strong>Erased.</strong> {erased.tables.map((t) => `${t.rows} rows from ${t.table}`).join(", ") || "nothing to delete"};
          {" "}{erased.deliveries.length} deliveries had carried their data. Due by {erased.dueBy}; recorded as {erased.id}, without the ID.
          {erased.agesOutBy && <> The tables' old versions hold the rows until about {erased.agesOutBy}{(erased.closes ?? []).length > 0 ? `, and the closes ${erased.closes!.join(", ")} keep theirs` : ""}.</>}
          {erased.errors && <div className="small">Not done: {Object.entries(erased.errors).map(([k, v]) => `${k}: ${v}`).join("; ")}</div>}
        </div>
      )}
      <div className="small muted">
        Each run of a delivery of personal data is run again on the tables as they were when it read them, for this person's rows.
        The log keeps no one's ID; the search isn't recorded with it either.
      </div>
      <ErrorBanner error={error} />
      {result && (
        <>
          <div className="small">
            {result.runs.filter((r) => !r.error).length} runs sent {result.person}'s data; {result.checked} runs checked
            {result.notKept > 0 ? `; ${result.notKept} not checkable (not personal, or from before reads were kept)` : ""}.
          </div>
          {result.runs.length > 0 && (
            <table className="t">
              <thead><tr><th>Delivery</th><th>Sent</th><th>To</th><th>Their rows</th><th>Approved by</th></tr></thead>
              <tbody>
                {result.runs.map((r) => (
                  <tr key={r.delivery + r.sent}>
                    <td className="mono small">{r.delivery}</td>
                    <td className="small">{r.date}</td>
                    <td className="mono small">{r.target}</td>
                    <td className="small">{r.error ? <span title={r.error}>unclear</span> : r.rows}</td>
                    <td className="small">{[...(r.approval?.by ?? []), ...(r.approval?.protection ?? [])].join(", ")}</td>
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
