import { useEffect, useState } from "react";
import { AuditEvent, auditCSV, AuditFilter, getAudit, getProjectAudit } from "../api";
import { Empty, Intro } from "../components";
import { ErrorBanner, Spinner, usePlatform, useProject } from "../lib";

// AuditLog is who did what: in the platform (a project changed, an alert
// muted), and in Trino, every access Ranger allowed or refused. A project's
// is its own and its tables'; the administrators' is the whole platform's.
export function AuditLog({ project }: { project?: string }) {
  const { admin } = usePlatform();
  const [filter, setFilter] = useState<AuditFilter>({ result: "" });
  const [applied, setApplied] = useState<AuditFilter>({ result: "" });
  const [events, setEvents] = useState<AuditEvent[]>();
  const [more, setMore] = useState(false);
  const [enabled, setEnabled] = useState(true);
  const [error, setError] = useState<string>();
  const load = async (f: AuditFilter, append: boolean) => {
    setError(undefined);
    try {
      const r = project ? await getProjectAudit(project, f) : await getAudit(f);
      setEnabled(r.enabled);
      setEvents((old) => (append ? [...(old ?? []), ...r.events] : r.events));
      setMore(!!r.more);
    } catch (e) {
      setError((e as Error).message);
    }
  };
  useEffect(() => { load(applied, false); }, [project, applied]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!enabled) return <div className="page"><Empty>The audit log isn't set up on this install (no AUDIT_URL).</Empty></div>;
  const csv = auditCSV({ ...applied, project });
  return (
    <div className="page stack" data-testid="audit">
      <Intro actions={admin && <a className="btn" href={csv} data-testid="audit-csv">Export CSV</a>}>
        {project
          ? <>Who changed this project, and every access to its tables that Trino allowed or refused, as Ranger decided it.</>
          : <>What people did in the platform, and every access Trino allowed or refused, as Ranger decided it.</>}
      </Intro>
      <form className="fields" onSubmit={(e) => { e.preventDefault(); setApplied({ ...filter }); }}>
        <label>Person<input placeholder="anyone" value={filter.user ?? ""} onChange={(e) => setFilter({ ...filter, user: e.target.value })} style={{ width: 140 }} /></label>
        <label>Search<input placeholder="Table, action or query" value={filter.q ?? ""} onChange={(e) => setFilter({ ...filter, q: e.target.value })} style={{ width: 220 }} /></label>
        <label>Result<select value={filter.result ?? ""} onChange={(e) => setFilter({ ...filter, result: e.target.value as AuditFilter["result"] })}>
          <option value="">Allowed and refused</option>
          <option value="denied">Refused only</option>
          <option value="allowed">Allowed only</option>
        </select></label>
        <label>Source<select value={filter.source ?? ""} onChange={(e) => setFilter({ ...filter, source: e.target.value })}>
          <option value="">The platform and Trino</option>
          <option value="platform">The platform's</option>
          <option value="trino">Trino's</option>
        </select></label>
        <label>From
          <input type="date" value={day(filter.since)} onChange={(e) => setFilter({ ...filter, since: e.target.value ? new Date(e.target.value).toISOString() : undefined })} />
        </label>
        <label>To
          <input type="date" value={day(filter.before, -1)} onChange={(e) => setFilter({ ...filter, before: e.target.value ? new Date(new Date(e.target.value).getTime() + 86_400_000).toISOString() : undefined })} />
        </label>
        <button className="btn primary" type="submit">Show</button>
      </form>
      <ErrorBanner error={error} />
      {!events ? <Spinner /> : events.length === 0 ? <Empty>Nothing recorded that matches.</Empty> : (
        <div className="card flush scroll-x"><table className="t" data-testid="audit-events">
          <thead><tr><th>When</th><th>Who</th><th>What</th><th>On</th><th>Result</th></tr></thead>
          <tbody>
            {events.map((e, i) => (
              <tr key={i}>
                <td className="small" style={{ whiteSpace: "nowrap" }}>{new Date(e.time).toLocaleString()}</td>
                <td>{e.user}{e.ip && <div className="muted small">{e.ip}</div>}</td>
                <td><span className="mono small">{e.action}</span>{e.detail && <div className="muted small" style={{ maxWidth: 420, overflowWrap: "anywhere" }}>{e.detail}</div>}</td>
                <td className="mono small" style={{ overflowWrap: "anywhere" }}>{e.resource}<div className="muted">{e.source}</div></td>
                <td>
                  <span className={`pill ${e.allowed ? "" : "red"}`}>{e.allowed ? "allowed" : "refused"}</span>
                  {e.policy ? <div className="muted small">policy {e.policy}</div> : null}
                  {e.reason && <div className="muted small">{e.reason}</div>}
                </td>
              </tr>
            ))}
          </tbody>
        </table></div>
      )}
      {more && events && (
        <div><button className="btn" onClick={() => load({ ...applied, before: events[events.length - 1].time }, true)}>Older</button></div>
      )}
    </div>
  );
}

// day is an ISO time as a date input's value, moved by days (the "to" date is
// the day before the time it stands for: up to its end).
const day = (iso?: string, by = 0) => (iso ? new Date(new Date(iso).getTime() + by * 86_400_000).toISOString().slice(0, 10) : "");

export function ProjectAudit() {
  const { name } = useProject();
  return <AuditLog project={name} />;
}

export function AdminAudit() {
  return <AuditLog />;
}
