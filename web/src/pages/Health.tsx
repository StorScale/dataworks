import { useState } from "react";
import { ago, getHealth, HealthService, muteAlerts, testAlert, when } from "../api";
import { ErrorBanner, Spinner, usePoll } from "../lib";

// Health is the platform's services, as the platform checks them every few
// seconds: up or down, why, what people lose, and the alerts sent about them.
export function AdminHealth() {
  const { data, error, reload } = usePoll(getHealth, [], 10_000);
  const [sent, setSent] = useState<string>();
  const [actionError, setActionError] = useState<string>();
  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  if (!data.checked) return <div className="page"><div className="empty">The platform doesn't check its services here (HEALTH isn't on).</div></div>;

  const services = data.services ?? [];
  const down = services.filter((s) => s.state === "down");
  const ends = when(data.certificate?.ends);
  const daysLeft = ends ? Math.floor((ends.getTime() - Date.now()) / 86_400_000) : undefined;
  const act = async (f: () => Promise<unknown>) => {
    setActionError(undefined);
    try { await f(); reload(); } catch (e) { setActionError((e as Error).message); }
  };
  const mute = (s: HealthService, hours: number) => act(() => muteAlerts(s.name, hours));
  const test = () => act(async () => {
    const { targets } = await testAlert();
    const parts = Object.entries(targets).map(([k, v]) => `${k}: ${v}`);
    setSent(parts.length ? parts.join("; ") : "No alert targets are set (the Secret storscale-alerts).");
  });

  return (
    <div className="page stack">
      <div className={`banner ${down.length ? "error" : "info"}`} data-testid="health-summary">
        {down.length === 0
          ? <>All {services.length} services are up.</>
          : <><strong>{down.length === 1 ? "1 service is" : `${down.length} services are`} down.</strong> People can't use: {data.affected.join(", ")}.</>}
      </div>
      <ErrorBanner error={actionError} />

      <div className="card flush">
        <table className="t" data-testid="health-services">
          <thead><tr><th>Service</th><th>State</th><th>Why</th><th>Used for</th><th>Alerts</th></tr></thead>
          <tbody>
            {services.map((s) => (
              <tr key={s.name} data-testid={`service-${s.name}`}>
                <td><strong>{s.label}</strong><div className="mono small muted">{s.name}</div></td>
                <td>
                  <span className={`pill ${s.state === "up" ? "green" : s.state === "down" ? "red" : ""}`}>{s.state}</span>
                  <div className="small muted">{s.state !== "unknown" && `since ${ago(when(s.since))}`}</div>
                </td>
                <td className="small">{s.reason || "–"}
                  {s.pods?.map((p) => <div key={p.name} className="small muted mono">{p.name}{p.node && ` on ${p.node}`}: {p.phase}</div>)}
                </td>
                <td className="small muted">{s.features.join(", ")}</td>
                <td className="small">
                  {s.mutedUntil
                    ? <>Muted until {when(s.mutedUntil)?.toLocaleTimeString()} <button className="btn small" onClick={() => mute(s, 0)}>Unmute</button></>
                    : <button className="btn small" onClick={() => mute(s, 4)} data-testid={`mute-${s.name}`}>Mute 4 h</button>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="grid main-side">
        <div className="card">
          <div className="card-head"><h2>Recent alerts</h2></div>
          {(data.alerts ?? []).length === 0 ? <p className="small muted">None since platformd started.</p> : (
            <ul className="plain small" data-testid="health-alerts">
              {data.alerts!.map((a, i) => (
                <li key={i}><strong>{a.title}</strong> <span className="muted">{ago(when(a.at))}</span>
                  {a.facts.length > 0 && <div className="muted">{a.facts.map(([k, v]) => `${k}: ${v}`).join(" · ")}</div>}
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="card stack">
          <div>
            <h2>Where alerts go</h2>
            <p className="small" style={{ margin: "6px 0 0" }}>
              Teams: {data.targets?.teams ? "a channel's webhook is set" : "not set"}.{" "}
              Email: {data.targets?.email?.length ? data.targets.email.join(", ") : "not set"}.
            </p>
            {data.targets?.error && <p className="small muted">{data.targets.error}</p>}
            <p className="small muted" style={{ margin: "6px 0 0" }}>Set in the platform's Secret <code>storscale-alerts</code> (teamsWebhook, emailTo, smtpServer, smtpFrom).</p>
            <button className="btn" onClick={test} data-testid="test-alert">Send a test alert</button>
            {sent && <p className="small" data-testid="test-alert-result">{sent}</p>}
          </div>
          {ends && (
            <div>
              <h2>The platform's certificate</h2>
              <p className="small" style={{ margin: "6px 0 0" }}>Ends {ends.toLocaleDateString()}{daysLeft !== undefined && ` (${daysLeft} days)`}; alerts start 14 days before.</p>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// HealthNotice tells everyone what they can't use just now.
export function HealthNotice() {
  const { data } = usePoll(getHealth, [], 30_000);
  if (!data?.affected?.length) return null;
  return (
    <div className="banner warn" data-testid="health-notice">
      Some of the platform isn't available just now: {data.affected.join(", ")}. Its administrators have been told.
    </div>
  );
}
