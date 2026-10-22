import { useMemo, useState } from "react";
import { ErrorBanner, Spinner, usePlatform, usePoll } from "../lib";

// The API reference (use case 14.3): platformd's routes, as /api/openapi.json
// describes them (made from the routes and their handlers' comments), grouped
// by what they're about, with how to sign in and the Python package.

type Operation = { operationId: string; summary: string; description?: string; tags: string[]; parameters?: { name: string }[]; requestBody?: unknown };
type Spec = { info: { title: string; description: string }; paths: Record<string, Record<string, Operation>> };

const getSpec = async (): Promise<Spec> => {
  const r = await fetch("/api/openapi.json");
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return r.json();
};

export default function Reference() {
  const { brand } = usePlatform();
  const { data, error } = usePoll(getSpec, [], 0);
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<string>();
  const groups = useMemo(() => {
    const out: Record<string, { method: string; path: string; op: Operation }[]> = {};
    for (const [path, ops] of Object.entries(data?.paths ?? {})) {
      for (const [method, op] of Object.entries(ops)) {
        const hay = `${method} ${path} ${op.summary} ${op.description ?? ""}`.toLowerCase();
        if (q && !q.toLowerCase().split(/\s+/).every((w) => hay.includes(w))) continue;
        (out[op.tags[0]] ??= []).push({ method: method.toUpperCase(), path, op });
      }
    }
    return Object.entries(out).sort(([a], [b]) => a.localeCompare(b));
  }, [data, q]);
  const origin = window.location.origin;
  return (
    <div className="page stack" data-testid="reference">
      <div className="phead"><div className="inner"><div className="title"><h1>API reference</h1>
        <span className="muted">{brand.name}'s API, as the web app, the assistants and the Python package use it</span></div></div></div>
      <div className="card stack" style={{ gap: 6 }}>
        <p style={{ margin: 0 }}>{data?.info.description}</p>
        <p className="small" style={{ margin: 0 }}>The document itself: <a href="/api/openapi.json" className="mono">{origin}/api/openapi.json</a> (OpenAPI 3.1), for any client generator.</p>
        <strong>From Python</strong>
        <pre className="mono small" style={{ margin: 0, whiteSpace: "pre-wrap" }}>{`pip install "git+https://github.com/StorScale/storscale-platform#subdirectory=stack/python"

import storscale
p = storscale.Platform("${origin}")          # asks for your name and password, or reads STORSCALE_TOKEN
p.projects()                                    # the projects you may see
p.call("get_projects_name_usage", name="sales") # any route, by its operation ID
run = p.start_run("sales", "flow_weekly")       # a pipeline run, as the project's pipelines
print(p.run_log("sales", "flow_weekly", run))`}</pre>
      </div>
      <div className="row">
        <input className="grow" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search: knowledge search, usage, approve…" aria-label="Search the API" data-testid="reference-search" />
        {data && <span className="small muted">{Object.values(data.paths).reduce((n, ops) => n + Object.keys(ops).length, 0)} routes</span>}
      </div>
      <ErrorBanner error={error} />
      {!data ? (!error && <Spinner />) : groups.map(([tag, ops]) => (
        <div key={tag} className="card stack" style={{ gap: 2 }}>
          <strong>{tag}</strong>
          {ops.map(({ method, path, op }) => (
            <div key={method + path}>
              <button type="button" className="row small" style={{ gap: 8, width: "100%", textAlign: "left", background: "none", border: 0, padding: "4px 0", cursor: "pointer" }}
                onClick={() => setOpen(open === op.operationId ? undefined : op.operationId)} aria-expanded={open === op.operationId}>
                <span className={`pill ${method === "GET" ? "" : method === "DELETE" ? "red" : "amber"}`} style={{ minWidth: 56, textAlign: "center" }}>{method}</span>
                <span className="mono">{path}</span>
                <span className="muted">{op.summary}</span>
              </button>
              {open === op.operationId && (
                <div className="small" style={{ margin: "4px 0 8px 64px", whiteSpace: "pre-wrap" }}>
                  {op.description}
                  <div className="muted" style={{ marginTop: 4 }}>Operation ID <span className="mono">{op.operationId}</span>
                    {op.requestBody ? "; a JSON body" : ""}</div>
                </div>)}
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}
