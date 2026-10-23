import { createContext, useContext, useEffect, useState } from "react";
import { Brand, FlowCheck, ProjectView, Spec, Tool, User } from "./api";

// What every page may need: who's signed in, and what the platform has.
export type Platform = { user: User; tools: Tool[]; admin: boolean; projects: boolean; flow: boolean; semantic: boolean; pipelines: boolean; brand: Brand; usage?: boolean; governance?: boolean };
export const PlatformContext = createContext<Platform>(null!);
export const usePlatform = () => useContext(PlatformContext);

// The project a /p/:project page is in.
export type ProjectCtx = { name: string; view?: ProjectView; error?: string; reload: () => void };
export const ProjectContext = createContext<ProjectCtx>(null!);
export const useProject = () => useContext(ProjectContext);

// The project someone was last in, for the switcher and Home's "continue".
const LAST = "storscale.lastProject";
export function lastProject(): string | undefined {
  try {
    return localStorage.getItem(LAST) ?? undefined;
  } catch {
    return undefined;
  }
}
export function rememberProject(name: string) {
  try {
    localStorage.setItem(LAST, name);
  } catch {
    /* private window: nothing to remember */
  }
}

// usePoll loads now, then every `every` ms while the page is open.
export function usePoll<T>(load: () => Promise<T>, deps: unknown[], every = 0) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<string>();
  const [n, setN] = useState(0);
  useEffect(() => {
    let live = true;
    const go = () => load().then((d) => { if (live) { setData(d); setError(undefined); } }).catch((e) => live && setError(e.message ?? String(e)));
    go();
    const t = every ? setInterval(go, every) : undefined;
    return () => { live = false; if (t) clearInterval(t); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, n]);
  return { data, error, reload: () => setN((x) => x + 1) };
}

export function initials(u: User) {
  const n = (u.name || u.username).trim().split(/\s+/);
  return ((n[0]?.[0] ?? "") + (n.length > 1 ? n[n.length - 1][0] : "")).toUpperCase();
}

export function roleLabel(role?: string, admin?: boolean) {
  if (role === "editor") return "Editor";
  if (role === "reader") return "Reader";
  if (role) return role.charAt(0).toUpperCase() + role.slice(1); // a role the administrators define (16.1)
  return admin ? "Administrator" : "No role";
}

// can says whether the viewer may do something in the project: by the permissions of their role there
// (16.1; an administrator, everything), as the platform sends them.
export function can(view: ProjectView | undefined, permission: string) {
  return !!view && (view.role === "editor" || !!view.can?.includes(permission));
}

export function RolePill({ role, admin }: { role?: string; admin?: boolean }) {
  return <span className={`pill ${role === "editor" ? "blue" : role === "reader" ? "" : "outline"}`} data-testid="role">{roleLabel(role, admin)}</span>;
}

export function Phase({ v }: { v: ProjectView }) {
  const phase = v.status?.phase ?? (v.spec ? "Waiting" : "Deleting");
  const cls = phase === "Ready" ? "pill green" : phase === "Error" ? "pill red" : "pill";
  return <span className={cls} data-testid={`phase-${v.name}`}>{phase === "Waiting" ? "Waiting for the operator" : phase}</span>;
}

export function CheckPill({ status }: { status: string }) {
  const cls = status === "Success" ? "pill green" : status === "Failed" ? "pill red" : "pill";
  return <span className={cls}>{status === "Success" ? "passing" : status === "Failed" ? "failing" : status ? status.toLowerCase() : "not run yet"}</span>;
}

export function StateDot({ state }: { state?: string }) {
  return <span className={`dot ${state === "success" ? "green" : state === "failed" ? "red" : state === "running" || state === "queued" ? "blue" : ""}`} />;
}

export const failing = (checks: FlowCheck[]) => checks.filter((c) => c.status === "Failed");

export const checkName = (project: string, table: string, check: string) => check.replace(`${project}_${table}_`, "");

export const namespaceOf = (name: string, spec?: Spec) =>
  spec?.tables ? `${spec.tables.catalog ?? "iceberg"}.${spec.tables.namespace ?? name.replace(/-/g, "_")}` : undefined;

// What the project's readers can't see of a table: the tables they may read, row
// filters, masked columns.
export function limits(spec: Spec | undefined, table: string) {
  const r = spec?.tables?.readers;
  return {
    hidden: !!r?.tables?.length && !r.tables.includes(table),
    filters: (r?.rowFilters ?? []).filter((f) => f.table === table).map((f) => f.filter),
    masks: Object.fromEntries((r?.masks ?? []).filter((m) => m.table === table).map((m) => [m.column, m.type])) as Record<string, string>,
  };
}

export const maskLabel = (t: string) =>
  ({ MASK_SHOW_LAST_4: "last 4 only", MASK_SHOW_FIRST_4: "first 4 only", MASK_HASH: "hashed", MASK_NULL: "hidden", MASK_DATE_SHOW_YEAR: "year only", MASK_NONE: "shown" })[t] ?? "masked";

// What each role means in each system, for a project with this spec.
export function grants(name: string, spec: Spec) {
  const ns = namespaceOf(name, spec);
  const bucket = spec.files ? spec.files.bucket || name : null;
  const r = spec.tables?.readers;
  const readerTables = r?.tables?.length ? r.tables.join(", ") : "every table";
  const cat = (x: { name: string }) => `${name.replace(/-/g, "_")}_${x.name}`;
  const sources = (spec.sources ?? []).filter((x) => x.name);
  const copies = sources.flatMap((x) => (x.copy?.tables ?? []).map((t) => `${x.name}_${t.split(".").pop()}`));
  const read = (xs: typeof sources) => (xs.length ? [`Read the source${xs.length > 1 ? "s" : ""} ${xs.map(cat).join(", ")} (not write)`] : []);
  const narrowing = [
    ...(r?.rowFilters ?? []).map((f) => `${f.table}: only rows where ${f.filter}`),
    ...(r?.masks ?? []).map((m) => `${m.table}.${m.column} masked (${maskLabel(m.type)})`),
  ];
  return [
    {
      role: "Editors",
      key: "editor",
      group: `${name}-editors`,
      items: [ns && `Create, change and drop tables in ${ns}`, bucket && `Read and write the ${bucket} bucket`, spec.pipelines && "Run the project's pipelines", ...read(sources),
        ...(copies.length ? [`Copies of sources' tables in ${ns ?? name}: ${copies.join(", ")}, as any table of the project`] : []),
        ...(spec.imports?.length ? [`Run its imports: ${spec.imports.map((x) => x.name).filter(Boolean).join(", ")} (its pipelines)`] : []), "Change the project's metrics"].filter(Boolean) as string[],
    },
    {
      role: "Readers",
      key: "reader",
      group: `${name}-readers`,
      items: [ns && `Read ${readerTables} in ${ns}`, ...narrowing, bucket && `Read the ${bucket} bucket`, ...read(sources.filter((x) => x.readers)),
        ...sources.filter((x) => x.readers).flatMap((x) => [
          ...(x.rowFilters ?? []).map((f) => `${cat(x)}.${f.table}: only rows where ${f.filter}`),
          ...(x.masks ?? []).map((m) => `${cat(x)}.${m.table}.${m.column} masked (${maskLabel(m.type)})`),
        ]), "Ask for the project's metrics"].filter(Boolean) as string[],
    },
    ...(spec.pipelines
      ? [{
          role: "Pipelines",
          key: "pipelines",
          group: `${name}-pipelines`,
          items: [
            `Keycloak client ${spec.pipelines.serviceAccount}'s service account`,
            ns && `Read, insert and delete in ${ns}'s tables`,
            bucket && `Read and write ${bucket}/landing/`,
            ...read(sources),
          ].filter(Boolean) as string[],
        }]
      : []),
  ];
}

export function Spinner() {
  return <div className="spinner" role="status" aria-label="Loading" />;
}

export function ErrorBanner({ error }: { error?: string }) {
  return error ? <div className="banner error" role="alert">{error}</div> : null;
}
