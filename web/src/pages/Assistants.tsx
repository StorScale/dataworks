import { useState } from "react";
import { ago, Assistant, getAssistants, putAssistant, removeAssistant, when } from "../api";
import { ErrorBanner, Spinner, usePoll } from "../lib";

// AdminAssistants registers the assistants the company approves to use the
// platform over MCP as the person who signs in (use case 10.4): each gets a
// Keycloak client from the operator, and its calls are audited under its name.
export default function AdminAssistants() {
  const { data, error, reload } = usePoll(getAssistants, [], 10_000);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [uris, setUris] = useState("");
  const [problem, setProblem] = useState<string>();
  const [busy, setBusy] = useState(false);
  if (error) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  const act = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setProblem(undefined);
    try {
      await f();
      reload();
    } catch (e) {
      setProblem(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const register = () => act(async () => {
    await putAssistant({ name: name.trim(), description: description.trim(), redirectUris: uris.split(/\s+/).filter(Boolean) });
    setName(""); setDescription(""); setUris("");
  });
  return (
    <div className="page stack" data-testid="assistants">
      <div className="page-head">
        <p className="lead" style={{ margin: 0 }}>
          An assistant the company approves (DevAssist, say) signs people in through Keycloak and asks the platform's MCP server for the
          business terms, metrics and data products, as them: each answer is theirs to see, and the audit log names the assistant and the person.
          Claude and IDEs use the built-in client, <code>storscale-agent</code>.
        </p>
      </div>
      {!data.available && <p className="muted">This install has no file store, so assistants can't be registered here.</p>}
      <div className="card stack" style={{ gap: 8 }}>
        <h3 style={{ margin: 0 }}>Register an assistant</h3>
        <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
          <input placeholder="Name, as devassist" value={name} onChange={(e) => setName(e.target.value)} aria-label="Name" style={{ width: 180 }} data-testid="assistant-name" />
          <input placeholder="What it is (optional)" value={description} onChange={(e) => setDescription(e.target.value)} aria-label="Description" style={{ width: 260 }} />
          <input placeholder="Redirect URIs, space-separated: https://assistant.example.com/callback" value={uris} onChange={(e) => setUris(e.target.value)} aria-label="Redirect URIs" style={{ flex: 1, minWidth: 320 }} data-testid="assistant-uris" />
          <button className="btn" onClick={register} disabled={busy || !data.available || !name.trim() || !uris.trim()} data-testid="assistant-register">Register</button>
        </div>
        <p className="small muted" style={{ margin: 0 }}>
          The assistant signs in with OAuth (authorization code with PKCE, a public client) and asks for the MCP server at its address below.
          Keycloak sends the person back only to a redirect URI listed here; <code>http://localhost:*</code> is for one running on the person's own machine.
        </p>
        {problem && <ErrorBanner error={problem} />}
      </div>
      {data.assistants.length === 0 ? <p className="muted">No assistant is registered yet.</p> : (
        <div className="card flush scroll-x">
          <table className="t">
            <thead><tr><th>Assistant</th><th>Client ID</th><th>MCP server</th><th>Redirect URIs</th><th>Registered</th><th></th></tr></thead>
            <tbody>
              {data.assistants.map((a: Assistant) => (
                <tr key={a.name} data-testid={`assistant-${a.name}`}>
                  <td><strong>{a.name}</strong>{a.description && <div className="small muted">{a.description}</div>}</td>
                  <td className="mono small">{a.clientId}</td>
                  <td className="mono small">{a.mcpUrl}</td>
                  <td className="mono small" style={{ overflowWrap: "anywhere" }}>{a.redirectUris.join(" ")}</td>
                  <td className="small muted">{a.addedBy} {a.addedAt && ago(when(a.addedAt))}</td>
                  <td><button className="btn small" onClick={() => act(() => removeAssistant(a.name))} disabled={busy} data-testid={`assistant-remove-${a.name}`}>Remove</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
