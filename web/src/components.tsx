import { ReactNode, useEffect, useRef, useState } from "react";
import { Link, NavLink, Outlet, useMatch, useNavigate, useParams } from "react-router-dom";
import { ApiError, getFlow, getPipelines, getProject, getSemantic, listProjects, ProjectView, signOut } from "./api";
import { initials, lastProject, Phase, ProjectContext, rememberProject, RolePill, Spinner, usePlatform, usePoll } from "./lib";

// useMenu is a menu that closes when someone clicks elsewhere or presses Escape.
function useMenu() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", away);
    document.addEventListener("keydown", esc);
    return () => { document.removeEventListener("mousedown", away); document.removeEventListener("keydown", esc); };
  }, [open]);
  return { open, setOpen, ref };
}

// TopBar is on every page: the platform, the project you're in, search, your tools, you.
export function TopBar() {
  const { user, admin, projects, brand } = usePlatform();
  const project = useMatch("/p/:project/*")?.params.project;
  const account = useMenu();
  return (
    <header className="topbar">
      <Link to="/" className="brand" data-testid="home-link">
        {brand.logo ? <img className="logo" src={brand.logo} alt={brand.name} /> : <><img src="/favicon.svg" alt="" /> {brand.name}</>}
      </Link>
      {projects && <ProjectSwitcher current={project} />}
      <Search project={project} />
      <span className="grow" />
      <ToolsMenu />
      <div className="account" ref={account.ref}>
        <button onClick={() => account.setOpen(!account.open)} aria-expanded={account.open} aria-haspopup="menu" data-testid="whoami">
          <span className="avatar" aria-hidden>{initials(user)}</span>
          <span>{user.name || user.username}</span>
        </button>
        <div className="menu" role="menu" hidden={!account.open}>
          <div className="section">Signed in as {user.username}</div>
          <div className="small muted" style={{ padding: "0 12px 8px" }}>{user.groups.join(", ") || "no groups"}</div>
          {admin && <Link to="/admin" onClick={() => account.setOpen(false)} data-testid="nav-admin">Administration</Link>}
          <button onClick={signOut} data-testid="sign-out">Sign out</button>
        </div>
      </div>
    </header>
  );
}

function ProjectSwitcher({ current }: { current?: string }) {
  const menu = useMenu();
  const { admin } = usePlatform();
  const [list, setList] = useState<ProjectView[]>();
  useEffect(() => {
    if (menu.open) listProjects().then((r) => setList(r.projects)).catch(() => setList([]));
  }, [menu.open]);
  const shown = current ?? lastProject();
  return (
    <div style={{ position: "relative" }} ref={menu.ref}>
      <button className="bar-button" onClick={() => menu.setOpen(!menu.open)} aria-haspopup="menu" aria-expanded={menu.open} data-testid="project-switcher">
        <span className="label">Project</span> <strong>{current ?? "None"}</strong> <span aria-hidden>▾</span>
      </button>
      {menu.open && (
        <div className="menu" role="menu">
          <div className="section">Your projects</div>
          {list === undefined ? <div style={{ padding: 12 }}><Spinner /></div> : list.filter((p) => p.role || admin).map((p) => (
            <Link key={p.name} to={`/p/${p.name}`} className={p.name === shown ? "active" : ""} onClick={() => menu.setOpen(false)} data-testid={`switch-${p.name}`}>
              <span>{p.name}</span><span className="muted small">{p.role ?? "admin"}</span>
            </Link>
          ))}
          {list?.length === 0 && <div className="small muted" style={{ padding: 12 }}>You're not in a project yet.</div>}
          <div className="section">All</div>
          <Link to="/" onClick={() => menu.setOpen(false)}>Home</Link>
          {admin && <Link to="/admin/projects" onClick={() => menu.setOpen(false)}>Manage projects</Link>}
        </div>
      )}
    </div>
  );
}

// ToolsMenu: the tools themselves, for work the platform's own pages don't cover.
function ToolsMenu() {
  const { tools } = usePlatform();
  const menu = useMenu();
  return (
    <div style={{ position: "relative" }} ref={menu.ref}>
      <button className="bar-button" onClick={() => menu.setOpen(!menu.open)} aria-haspopup="menu" aria-expanded={menu.open} data-testid="tools-menu">
        Tools <span aria-hidden>▾</span>
      </button>
      <div className="menu" role="menu" style={{ right: 0, minWidth: 300 }} hidden={!menu.open}>
        {(["work", "admin"] as const).map((section) => {
          const ts = tools.filter((t) => t.section === section);
          return ts.length === 0 ? null : (
            <div key={section}>
              <div className="section">{section === "work" ? "Tools" : "Administration"}</div>
              {ts.map((t) => t.embedUrl ? (
                <Link key={t.id} to={`/tools/${t.id}`} onClick={() => menu.setOpen(false)} data-testid={`nav-${t.id}`}>
                  <span>{t.name}</span>
                </Link>
              ) : (
                <a key={t.id} href={t.url} target="_blank" rel="noreferrer" onClick={() => menu.setOpen(false)} data-testid={`nav-${t.id}`}>
                  <span>{t.name}</span><span className="muted" aria-label="opens in a new tab">↗</span>
                </a>
              ))}
            </div>
          );
        })}
      </div>
    </div>
  );
}

type Hit = { kind: string; label: string; sub?: string; to: string };

// Search finds a project's tables, columns, metrics and pipelines; outside a
// project, the projects.
function Search({ project }: { project?: string }) {
  const navigate = useNavigate();
  const { flow, semantic, pipelines } = usePlatform();
  const [q, setQ] = useState("");
  const [open, setOpen] = useState(false);
  const [index, setIndex] = useState<{ for: string; hits: Hit[] }>();
  const [sel, setSel] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const key = project ?? "";

  useEffect(() => {
    const slash = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if (e.key === "/" && !["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName) && !t.isContentEditable) {
        e.preventDefault();
        input.current?.focus();
      }
    };
    document.addEventListener("keydown", slash);
    return () => document.removeEventListener("keydown", slash);
  }, []);

  const load = async () => {
    if (index?.for === key) return;
    const hits: Hit[] = [];
    if (!project) {
      const r = await listProjects().catch(() => ({ projects: [] as ProjectView[] }));
      for (const p of r.projects) hits.push({ kind: "Project", label: p.name, sub: p.spec?.spec.description, to: `/p/${p.name}` });
    } else {
      const base = `/p/${project}`;
      const [f, s, ps] = await Promise.all([
        flow ? getFlow(project).catch(() => undefined) : undefined,
        semantic ? getSemantic(project).catch(() => undefined) : undefined,
        pipelines ? getPipelines(project).catch(() => undefined) : undefined,
      ]);
      for (const t of f?.tables.filter((t) => !t.external) ?? []) {
        hits.push({ kind: "Table", label: t.name, sub: t.columns.map((c) => c.name).join(", "), to: `${base}/data?table=${t.name}` });
        for (const c of t.columns) hits.push({ kind: "Column", label: `${t.name}.${c.name}`, sub: c.type, to: `${base}/data?table=${t.name}` });
      }
      for (const m of s?.metrics ?? []) hits.push({ kind: "Metric", label: m.name, sub: m.description, to: `${base}/metrics?metric=${m.name}` });
      for (const p of ps?.pipelines ?? []) hits.push({ kind: "Pipeline", label: p.id, sub: p.description, to: `${base}/pipelines?pipeline=${p.id}` });
    }
    setIndex({ for: key, hits });
  };

  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  const hits = !words.length || index?.for !== key ? [] : index.hits
    .filter((h) => words.every((w) => `${h.label} ${h.sub ?? ""}`.toLowerCase().includes(w)))
    .sort((a, b) => Number(!a.label.toLowerCase().startsWith(words[0])) - Number(!b.label.toLowerCase().startsWith(words[0])))
    .slice(0, 12);
  const go = (h: Hit) => { setOpen(false); setQ(""); input.current?.blur(); navigate(h.to); };

  return (
    <div className="search" role="search">
      <label>
        <span aria-hidden>⌕</span>
        <span className="sr">Search</span>
        <input
          ref={input}
          value={q}
          placeholder={project ? `Search ${project}: tables, columns, metrics, pipelines` : "Search projects"}
          onFocus={() => { setOpen(true); load(); }}
          onBlur={() => setTimeout(() => setOpen(false), 150)}
          onChange={(e) => { setQ(e.target.value); setSel(0); setOpen(true); }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") { e.preventDefault(); setSel((s) => Math.min(s + 1, hits.length - 1)); }
            if (e.key === "ArrowUp") { e.preventDefault(); setSel((s) => Math.max(s - 1, 0)); }
            if (e.key === "Enter" && hits[sel]) go(hits[sel]);
            if (e.key === "Escape") input.current?.blur();
          }}
          data-testid="search"
        />
        <kbd>/</kbd>
      </label>
      {open && words.length > 0 && (
        <div className="menu" role="listbox" data-testid="search-results">
          {index?.for !== key ? <div style={{ padding: 12 }}><Spinner /></div> : hits.length === 0 ? (
            <div className="small muted" style={{ padding: 12 }}>Nothing matches “{q}”.</div>
          ) : hits.map((h, i) => (
            <button key={h.kind + h.label} className={i === sel ? "active" : ""} onMouseDown={(e) => e.preventDefault()} onClick={() => go(h)} role="option" aria-selected={i === sel}>
              <span style={{ minWidth: 0 }}>
                <span className="mono">{h.label}</span>
                {h.sub && <span className="small muted" style={{ display: "block", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: 380 }}>{h.sub}</span>}
              </span>
              <span className="kind">{h.kind}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ProjectFrame is every /p/:project page: the project's header and tabs, then the page.
export function ProjectFrame() {
  const { project = "" } = useParams();
  const { admin, tools, flow, semantic, pipelines } = usePlatform();
  const { data: view, error, reload } = usePoll(() => getProject(project), [project], 10_000);
  useEffect(() => { if (view?.role || admin) rememberProject(project); }, [project, view, admin]);
  const spec = view?.spec?.spec;
  const has = (id: string) => tools.some((t) => t.id === id && t.embedUrl);
  const tabs: [string, string, boolean][] = [
    ["", "Overview", true],
    ["data", "Data", !!spec?.tables && flow],
    ["metrics", "Metrics", !!spec?.tables && semantic],
    ["sql", "SQL", !!spec?.tables && semantic],
    ["notebooks", "Notebooks", has("notebooks")],
    ["pipelines", "Pipelines", !!spec?.pipelines && pipelines],
    ["dashboards", "Dashboards", has("dashboards")],
    ["tags", "Tags", !!spec?.tables],
    ["access", "Access", true],
    ["audit", "Audit", view?.role === "editor" || admin],
  ];
  return (
    <ProjectContext.Provider value={{ name: project, view, error, reload }}>
      <div className="phead">
        <div className="inner">
          <div className="title">
            <h1 data-testid="project-name">{project}</h1>
            {view && <RolePill role={view.role} admin={admin} />}
            {view && view.status?.phase !== "Ready" && <Phase v={view} />}
            {spec?.description && <span className="muted">{spec.description}</span>}
          </div>
          <nav className="tabs" aria-label={`${project}`}>
            {tabs.filter((t) => t[2]).map(([path, label]) => (
              <NavLink key={label} to={path ? `/p/${project}/${path}` : `/p/${project}`} end={!path} data-testid={`tab-${label.toLowerCase()}`}>{label}</NavLink>
            ))}
          </nav>
        </div>
      </div>
      {error && !view ? (
        <div className="page"><div className="banner error" role="alert">{(error.includes("404") || error.includes("not found")) ? `There's no project called ${project}.` : error}</div></div>
      ) : (
        <Outlet />
      )}
    </ProjectContext.Provider>
  );
}

export function Page({ title, lead, actions, children, testid }: { title?: ReactNode; lead?: ReactNode; actions?: ReactNode; children: ReactNode; testid?: string }) {
  return (
    <div className="page" data-testid={testid}>
      {(title || actions) && (
        <div className="page-head">
          <div>
            {title && <h1>{title}</h1>}
            {lead && <p className="lead">{lead}</p>}
          </div>
          {actions && <div className="row">{actions}</div>}
        </div>
      )}
      {children}
    </div>
  );
}

export const isNotFound = (e: unknown) => e instanceof ApiError && e.status === 404;
