import { useState } from "react";
import { approveServiceAccount, getServiceAccounts, putServiceAccount, removeServiceAccount, rotateServiceAccount, type ServiceAccountSecret } from "../api";
import { ErrorBanner, usePoll } from "../lib";

// A project's service accounts: applications' identities in it (a CRM, a
// company system), each a member in a role. Its editors register them; an
// administrator other than the registrant approves, and the secret is shown
// once; editors revoke them at once.
export default function ServiceAccounts({ project, user, roles }: { project: string; user: string; roles: string[] }) {
  const list = usePoll(() => getServiceAccounts(project), [project], 15_000);
  const [error, setError] = useState<string>();
  const [adding, setAdding] = useState(false);
  const [shown, setShown] = useState<ServiceAccountSecret & { account: string }>();
  const [revoking, setRevoking] = useState<string>();
  if (!list.data) return null;
  const { serviceAccounts, canEdit, canApprove } = list.data;
  if (!canEdit && serviceAccounts.length === 0) return null;
  const act = async (f: () => Promise<unknown>) => { setError(undefined); try { await f(); list.reload(); } catch (e) { setError((e as Error).message); } };
  const secretOf = (account: string, f: () => Promise<ServiceAccountSecret>) => act(async () => { const r = await f(); if (r.secret) setShown({ ...r, account }); });
  return (
    <section className="card flush" data-testid="service-accounts">
      <div className="card-head">
        <h2>Service accounts</h2>
        {canEdit && !adding && <button className="btn small" onClick={() => setAdding(true)} data-testid="add-service-account">Register one</button>}
      </div>
      <div className="stack" style={{ padding: "0 20px 16px", gap: 10 }}>
        <p className="small muted" style={{ margin: 0 }}>
          Applications that work with the project as a member would, signed in as themselves with a secret (OAuth's client credentials): a CRM that keeps
          its metric definitions, a system that reads its tables. Each is a member in its role, here only. An administrator other than the one who
          registered it approves it.
        </p>
        <ErrorBanner error={error} />
        {shown && (
          <div className="banner info" data-testid="service-account-secret">
            <strong>{shown.account}'s secret</strong>, shown once: give it to the application now. Client <span className="mono">{shown.clientId}</span>,
            secret <span className="mono">{shown.secret}</span>; it gets a token at <span className="mono">{shown.tokenUrl}</span> with{" "}
            <span className="mono">grant_type=client_credentials</span>, and calls the API as <span className="mono">{shown.username}</span>.
            The operator makes its client within a minute.
            <div><button className="btn small" onClick={() => setShown(undefined)}>Done</button></div>
          </div>
        )}
        {adding && <Register project={project} roles={roles} taken={serviceAccounts.map((s) => s.name)} onDone={() => { setAdding(false); list.reload(); }} />}
        {serviceAccounts.length === 0 && !adding && <p className="small muted" style={{ margin: 0 }}>None yet.</p>}
        {serviceAccounts.map((s) => (
          <div key={s.name} className="row small" style={{ flexWrap: "wrap", gap: 8, alignItems: "center" }} data-testid={`service-account-${s.name}`}>
            <span className="mono"><strong>{s.name}</strong></span>
            <span className={`pill ${s.role === "editor" ? "blue" : ""}`}>{s.role}</span>
            <span className={"pill " + (s.approved ? "green" : "amber")}>{s.approved ? `approved by ${s.approvedBy}` : "awaiting an administrator's approval"}</span>
            <span className="muted">{s.description ? `${s.description}; ` : ""}owner {s.owner}; client <span className="mono">{s.clientId}</span></span>
            <span style={{ marginLeft: "auto" }} className="row">
              {canApprove && !s.approved && s.registeredBy !== user &&
                <button className="btn small primary" onClick={() => secretOf(s.name, () => approveServiceAccount(project, s.name))} data-testid={`approve-sa-${s.name}`}>Approve</button>}
              {canApprove && s.approved &&
                <button className="btn small" onClick={() => secretOf(s.name, () => rotateServiceAccount(project, s.name))} data-testid={`rotate-sa-${s.name}`}>New secret</button>}
              {canEdit && revoking !== s.name && <button className="btn small danger" onClick={() => setRevoking(s.name)} data-testid={`revoke-sa-${s.name}`}>Revoke</button>}
              {canEdit && revoking === s.name && <>
                <span>It signs in no more.</span>
                <button className="btn small danger" onClick={() => { setRevoking(undefined); act(() => removeServiceAccount(project, s.name)); }} data-testid={`confirm-revoke-sa-${s.name}`}>Revoke it</button>
                <button className="btn small" onClick={() => setRevoking(undefined)}>Keep it</button>
              </>}
            </span>
          </div>
        ))}
      </div>
    </section>
  );
}

function Register({ project, roles, taken, onDone }: { project: string; roles: string[]; taken: string[]; onDone: () => void }) {
  const [name, setName] = useState("");
  const [role, setRole] = useState("reader");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const ok = /^[a-z][a-z0-9-]{0,30}$/.test(name) && !taken.includes(name);
  const save = async () => {
    setBusy(true);
    setError(undefined);
    try { await putServiceAccount(project, { name, role, description: description || undefined }); onDone(); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <form className="stack" onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="service-account-form">
      <ErrorBanner error={error} />
      <div className="fields">
        <label>Name<input className="mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="crm" data-testid="service-account-name" /></label>
        <label>Role<select value={role} onChange={(e) => setRole(e.target.value)} data-testid="service-account-role">
          {roles.map((r) => <option key={r} value={r}>{r}</option>)}
        </select></label>
        <label className="grow">What it's for<input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="The CRM: keeps the sales metrics" /></label>
      </div>
      <div className="form-actions">
        <button className="btn primary" type="submit" disabled={!ok || busy} data-testid="register-service-account">{busy ? "Saving…" : "Register"}</button>
        <button className="btn" type="button" onClick={onDone}>Cancel</button>
        <span className="field-help">You're its owner. Its client is <span className="mono">svc-{project}-{name || "<name>"}</span>.</span>
      </div>
    </form>
  );
}
