import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { deleteLibrary, deleteTemplate, getTemplates, projectFromTemplate, putLibrary, putTemplate, type Library, type Member, type Template } from "../api";
import { Empty, Intro } from "../components";
import { ErrorBanner, Spinner, usePoll } from "../lib";

// Project templates and the company's shared libraries (use case U.2). A
// template is what a new team's project starts as: a spec, and a repo in the
// install's GitLab that the project gets a copy of ("{project}" in either
// becomes its name). A library is a repo at a ref that projects use without
// copying: a dbt package named in packages.yml, or Python code a pipeline or
// notebook puts on its path. Anyone signed in sees them; administrators
// change them and make projects from templates.
export default function AdminTemplates() {
  const { data, error, reload } = usePoll(getTemplates, [], 30_000);
  const [editing, setEditing] = useState<Template | "new">();
  const [editingLib, setEditingLib] = useState<Library | "new">();
  const [making, setMaking] = useState<Template>();
  const [actionError, setActionError] = useState<string>();
  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  const remove = async (kind: "template" | "library", name: string) => {
    if (!confirm(`Remove the ${kind} ${name}? ${kind === "template" ? "Projects made from it stay as they are." : "Projects naming it can't fetch it any more."}`)) return;
    setActionError(undefined);
    try { await (kind === "template" ? deleteTemplate(name) : deleteLibrary(name)); reload(); } catch (e) { setActionError((e as Error).message); }
  };
  return (
    <div className="page stack" data-testid="templates">
      <Intro actions={data.canEdit && !editing && !making && <button className="btn primary" onClick={() => setEditing("new")} data-testid="add-template">Define a template</button>}>
        What a new team's project starts as: its checks, its limits, where it's told of trouble, and a copy of the template's repo (a sample pipeline,
        a dbt model) as its own, in {data.repos ? <span className="mono">{data.gitlab}/{data.group}</span> : "the install's GitLab (none is set: templates.gitlab)"}.
        "{"{project}"}" in the template's spec or files becomes the project's name.
      </Intro>
      <ErrorBanner error={actionError} />
      {editing && <TemplateForm template={editing === "new" ? undefined : editing} repos={data.repos} onDone={() => { setEditing(undefined); reload(); }} />}
      {making && <MakeProject template={making} onCancel={() => setMaking(undefined)} />}
      {data.templates.length === 0 && !editing && <Empty inline>No templates yet.</Empty>}
      <div className="grid cards">
        {data.templates.map((t) => (
          <div key={t.name} className="card stack" style={{ gap: 8 }} data-testid={`template-${t.name}`}>
            <div className="card-head">
              <h2>{t.name}</h2>
              {data.canEdit && (
                <div className="row">
                  <button className="btn small primary" onClick={() => setMaking(t)} data-testid={`make-${t.name}`}>Make a project</button>
                  <button className="btn small" onClick={() => setEditing(t)}>Change</button>
                  <button className="btn small danger" onClick={() => remove("template", t.name)}>Remove</button>
                </div>
              )}
            </div>
            <div className="small">{t.description}</div>
            <ul className="small" style={{ margin: 0, paddingLeft: 18 }}>
              {says(t).map((x) => <li key={x}>{x}</li>)}
            </ul>
            {t.changedBy && <div className="small muted">Changed by {t.changedBy}{t.changedAt ? ", " + new Date(t.changedAt).toLocaleString() : ""}.</div>}
          </div>
        ))}
      </div>

      <div className="page-head" style={{ marginTop: 16 }}>
        <h2>Shared libraries</h2>
        {data.canEdit && !editingLib && data.repos && <button className="btn" onClick={() => setEditingLib("new")} data-testid="add-library">Add a library</button>}
      </div>
      <p className="small muted" style={{ margin: 0 }}>
        The company's code, kept once and used by every project without copying it: the platform reads it from GitLab at the ref given here,
        so a project needs no token for it. In a dbt project, name it in <span className="mono">packages.yml</span> by its address
        (<span className="mono">- git: "…/repo.git"</span>); in a pipeline, <span className="mono">storscale.library.use("name")</span>;
        in a notebook, <span className="mono">buckets_lake.library("name")</span>.
      </p>
      {editingLib && <LibraryForm library={editingLib === "new" ? undefined : editingLib} onDone={() => { setEditingLib(undefined); reload(); }} />}
      {data.libraries.length === 0 ? <Empty inline>{data.repos ? "No libraries yet." : "Libraries need the install's GitLab (templates.gitlab)."}</Empty> : (
        <div className="card flush">
          <table className="t">
            <thead><tr><th>Library</th><th>Repo</th><th>Ref</th><th>In packages.yml</th>{data.canEdit && <th />}</tr></thead>
            <tbody>
              {data.libraries.map((l) => (
                <tr key={l.name} data-testid={`library-${l.name}`}>
                  <td><strong>{l.name}</strong><div className="small muted">{l.description}</div></td>
                  <td className="mono small">{l.repo}</td>
                  <td className="mono small">{l.ref}</td>
                  <td className="mono small">{`${data.gitlab}/${l.repo}.git`}</td>
                  {data.canEdit && <td className="row">
                    <button className="btn small" onClick={() => setEditingLib(l)}>Change</button>
                    <button className="btn small danger" onClick={() => remove("library", l.name)}>Remove</button>
                  </td>}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// says is what a template gives a project, in words.
function says(t: Template): string[] {
  const s = t.spec, out: string[] = [];
  if (t.repo) out.push(`A copy of the repo ${t.repo}${s.pipelines?.path ? `, its pipelines in ${s.pipelines.path}/` : ""}`);
  if (s.tables) out.push(`Tables${s.tables.freshness?.length ? `, ${s.tables.freshness.length} freshness targets` : ""}${s.tables.checks?.length ? `, ${s.tables.checks.length} checks` : ""}`);
  if (s.files) out.push("A bucket of its own");
  if (s.limits) out.push("Limits: " + Object.entries(s.limits).map(([k, v]) => `${k} ${v}`).join(", "));
  if (s.notify) out.push("Told of trouble at " + [s.notify.email?.join(", "), s.notify.teams && "Teams", s.notify.slack && "Slack"].filter(Boolean).join(", "));
  return out;
}

const starterSpec = `{
  "description": "{project}: a new team's",
  "tables": {},
  "files": {},
  "pipelines": {"serviceAccount": "airflow-pipelines", "path": "dags"},
  "limits": {"storage": "100Gi"}
}`;

function TemplateForm({ template, repos, onDone }: { template?: Template; repos: boolean; onDone: () => void }) {
  const [name, setName] = useState(template?.name ?? "");
  const [description, setDescription] = useState(template?.description ?? "");
  const [repo, setRepo] = useState(template?.repo ?? "");
  const [spec, setSpec] = useState(template ? JSON.stringify(template.spec, null, 2) : starterSpec);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const save = async () => {
    setBusy(true); setError(undefined);
    try {
      await putTemplate(name, { description, repo: repo || undefined, spec: JSON.parse(spec) });
      onDone();
    } catch (e) { setError(e instanceof SyntaxError ? `The spec isn't JSON: ${e.message}` : (e as Error).message); } finally { setBusy(false); }
  };
  return (
    <form className="card stack" onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="template-form">
      <h2>{template ? `Change ${template.name}` : "Define a template"}</h2>
      <ErrorBanner error={error} />
      <div className="fields">
        <label>Name<input className="mono" value={name} disabled={!!template} onChange={(e) => setName(e.target.value)} placeholder="starter" data-testid="template-name" /></label>
        <label className="grow">What it's for<input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="A new team's project: a sample pipeline, a dbt model and the company's checks" /></label>
      </div>
      <label>Its repo in GitLab<input className="mono" value={repo} onChange={(e) => setRepo(e.target.value)} disabled={!repos} placeholder="data/templates/starter" data-testid="template-repo" />
        <span className="field-help">{repos ? "Each project made from it gets a copy, as its own. Empty: no repo." : "The install has no GitLab for templates (templates.gitlab)."}</span></label>
      <label>The project's spec<textarea className="mono" rows={12} value={spec} onChange={(e) => setSpec(e.target.value)} data-testid="template-spec" />
        <span className="field-help">As a project's (the Edit page's), without members or a repo: those come when a project is made.</span></label>
      <div className="form-actions">
        <button className="btn primary" type="submit" disabled={busy || !name || !description} data-testid="save-template">{busy ? "Saving…" : template ? "Save" : "Define it"}</button>
        <button className="btn" type="button" onClick={onDone}>Cancel</button>
      </div>
    </form>
  );
}

function MakeProject({ template, onCancel }: { template: Template; onCancel: () => void }) {
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [editors, setEditors] = useState("");
  const [readers, setReaders] = useState("");
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const list = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);
  const member = (role: string) => (x: string): Member => (x.startsWith("@") ? { user: x.slice(1), role } : { group: x, role });
  const make = async () => {
    setBusy(true); setError(undefined);
    try {
      await projectFromTemplate(template.name, { name, members: [...list(editors).map(member("editor")), ...list(readers).map(member("reader"))],
        notify: list(email).length ? { email: list(email) } : undefined });
      navigate(`/p/${name}`);
    } catch (e) { setError((e as Error).message); setBusy(false); }
  };
  return (
    <form className="card stack" onSubmit={(e) => { e.preventDefault(); make(); }} data-testid="make-project">
      <h2>A project from {template.name}</h2>
      <ErrorBanner error={error} />
      <div className="fields">
        <label>Name<input className="mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="growth" data-testid="make-name" /></label>
        <label className="grow">Editors<input className="mono" value={editors} onChange={(e) => setEditors(e.target.value)} placeholder="growth-team, @dana" data-testid="make-editors" /></label>
        <label className="grow">Readers<input className="mono" value={readers} onChange={(e) => setReaders(e.target.value)} placeholder="analysts" /></label>
      </div>
      <label>Told of trouble at<input value={email} onChange={(e) => setEmail(e.target.value)} placeholder={template.spec.notify?.email?.join(", ") ?? "growth-team@example.com"} />
        <span className="field-help">Keycloak groups, or people as @name, comma-separated. Empty: the template's channel.</span></label>
      <div className="form-actions">
        <button className="btn primary" type="submit" disabled={busy || !name || list(editors).length === 0} data-testid="make-it">
          {busy ? (template.repo ? "Copying its repo…" : "Making it…") : "Make it"}</button>
        <button className="btn" type="button" onClick={onCancel}>Cancel</button>
        {template.repo && <span className="field-help">Its repo is a copy of {template.repo}, with a token of its own.</span>}
      </div>
    </form>
  );
}

function LibraryForm({ library, onDone }: { library?: Library; onDone: () => void }) {
  const [name, setName] = useState(library?.name ?? "");
  const [description, setDescription] = useState(library?.description ?? "");
  const [repo, setRepo] = useState(library?.repo ?? "");
  const [ref, setRef] = useState(library?.ref ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const save = async () => {
    setBusy(true); setError(undefined);
    try { await putLibrary(name, { description, repo, ref }); onDone(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return (
    <form className="card stack" onSubmit={(e) => { e.preventDefault(); save(); }} data-testid="library-form">
      <h2>{library ? `Change ${library.name}` : "Add a library"}</h2>
      <ErrorBanner error={error} />
      <div className="fields">
        <label>Name<input className="mono" value={name} disabled={!!library} onChange={(e) => setName(e.target.value)} placeholder="company-dbt" /></label>
        <label className="grow">What it's for<input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="The company's dbt macros and checks" /></label>
      </div>
      <div className="fields">
        <label className="grow">Its repo in GitLab<input className="mono" value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="data/libraries/company-dbt" /></label>
        <label>Ref<input className="mono" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="v1.0.0" /></label>
      </div>
      <div className="form-actions">
        <button className="btn primary" type="submit" disabled={busy || !name || !description || !repo || !ref}>{busy ? "Saving…" : library ? "Save" : "Add it"}</button>
        <button className="btn" type="button" onClick={onDone}>Cancel</button>
        <span className="field-help">A tag is best: it doesn't move. Projects use the new ref at their next run.</span>
      </div>
    </form>
  );
}
