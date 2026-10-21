// The app's calls to platformd: who's signed in, and signing out.

export type User = { username: string; name: string; email: string; groups: string[] };

export type Tool = {
  id: string;
  name: string;
  description: string;
  section: "work" | "admin";
  url: string;
  embedUrl?: string;
};

// The top bar's corner: StorScale's icon and name, or the deployment's logo
// in their place (logo: its address), named for screen readers.
export type Brand = { name: string; logo: string };

export type Session =
  | { state: "signed-in"; user: User; tools: Tool[]; admin: boolean; projects: boolean; flow: boolean; semantic: boolean; pipelines: boolean; brand: Brand; usage?: boolean; governance?: boolean }
  | { state: "refused"; user: User; error: string };

// session is null when nobody is signed in.
export async function session(): Promise<Session | null> {
  const res = await fetch("/api/session", { headers: { "X-Platform-Request": "1" } });
  if (res.status === 401) return null;
  const body = await res.json();
  if (res.status === 403) return { state: "refused", user: body.user, error: body.error };
  if (!res.ok) throw new Error(body.error ?? `HTTP ${res.status}`);
  return { state: "signed-in", user: body.user, tools: body.tools, admin: !!body.admin, projects: !!body.projects, flow: !!body.flow, semantic: !!body.semantic, pipelines: !!body.pipelines,
    brand: { name: body.brand?.name || "StorScale", logo: body.brand?.logo || "" } };
}

// Set once signing out has begun: a request answered 401 after that (one
// still in flight when the session ended) must not start a sign-in, which
// would race the browser to Keycloak's sign-in page instead of its logout.
let signingOut = false;

export function signIn() {
  if (signingOut) return;
  const next = window.location.pathname + window.location.search;
  window.location.assign(`/auth/login?next=${encodeURIComponent(next)}`);
}

// signOut ends the platform's session, then Keycloak's.
export async function signOut() {
  signingOut = true;
  const res = await fetch("/auth/logout", { method: "POST", headers: { "X-Platform-Request": "1" } });
  const body = await res.json().catch(() => ({}));
  window.location.assign(body.redirect ?? "/");
}

// --- Projects -----------------------------------------------------------------------

export type Member = { group?: string; user?: string; role: "reader" | "editor" };
export type Spec = {
  description?: string;
  members: Member[];
  tables?: {
    catalog?: string;
    namespace?: string;
    readers?: {
      tables?: string[];
      rowFilters?: { table: string; filter: string }[];
      masks?: { table: string; column: string; type: string }[];
    };
    freshness?: { table: string; within: string }[];
    maintenance?: { paused?: boolean };
  };
  files?: { bucket?: string };
  pipelines?: { serviceAccount: string; repo?: string; path?: string; ref?: string; credentials?: { secretRef: string }; memory?: string;
    reviews?: { gitlab?: string; project?: string; dbt?: string } };
  limits?: { queries?: number; queued?: number; queryMemory?: string; storage?: string };
  notify?: { teams?: string; slack?: string; email?: string[] };
  sources?: Source[];
  imports?: Import[];
  deliveries?: { name: string }[];
  knowledge?: { name: string; description?: string }[];
};
// An application's data (or a database's, by a cursor), copied into the project's tables on a schedule (dlt).
export type APIResource = { name: string; path: string; dataSelector?: string; primaryKey?: string; cursor?: string; cursorParam?: string; cursorStart?: string };
export type Import = {
  name: string; source: "rest_api" | "sql_database" | "salesforce" | "files"; connection?: string; schedule?: string;
  files?: { tables: { name: string; file: string; primaryKey?: string }[] };
  api?: { baseUrl: string; resources: APIResource[] };
  database?: { kind: "postgresql" | "mysql"; tables: { table: string; cursor?: string; primaryKey?: string }[] };
  salesforce?: { objects: string[]; since?: string };
};
// The platform's connectors (8.4), and a project's editors' imports.
export type Connector = { source: "salesforce"; label: string; connection: string; objects: string[]; incremental: string[] };
export const getConnectors = () => api<{ connectors: Connector[] }>("GET", "/api/connectors");
export const putImport = (name: string, im: Import) =>
  api<Import>("PUT", `/api/projects/${encodeURIComponent(name)}/imports/${encodeURIComponent(im.name)}`, { ...im, name: undefined });
export const removeImport = (name: string, im: string) => api<void>("DELETE", `/api/projects/${encodeURIComponent(name)}/imports/${encodeURIComponent(im)}`);
// A company database the project queries where it is: a read-only Trino catalog <project>_<name>.
export type Source = {
  name: string; kind: "postgresql" | "mysql" | "sqlserver" | "oracle"; connection: string; readers?: boolean; schemas?: string[];
  masks?: { table: string; column: string; type: string }[]; rowFilters?: { table: string; filter: string }[];
  copy?: { tables: string[] };
};
export const sourceCatalog = (project: string, s: { name: string }) => `${project.replace(/-/g, "_")}_${s.name}`;
export type Project = { apiVersion: string; kind: string; metadata: { name: string }; spec: Spec };
export type Part = { system: string; ok: boolean; message: string };
export type Person = { username: string; role: string; via: string };
export type Status = { name: string; phase: string; parts: Part[]; members: Person[]; updated: string };
export type ProjectView = { name: string; spec?: Project; status?: Status; role?: string };

export const MASK_TYPES = ["MASK", "MASK_SHOW_LAST_4", "MASK_SHOW_FIRST_4", "MASK_HASH", "MASK_NULL", "MASK_NONE", "MASK_DATE_SHOW_YEAR"];

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: { "X-Platform-Request": "1", ...(body ? { "Content-Type": "application/json" } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) {
    signIn();
    throw new ApiError(401, "signed out");
  }
  if (res.status === 204) return undefined as T;
  const json = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, json.error ?? `HTTP ${res.status}`);
  return json as T;
}

export const listProjects = () => api<{ projects: ProjectView[]; admin: boolean }>("GET", "/api/projects");
export const getProject = (name: string) => api<ProjectView>("GET", `/api/projects/${encodeURIComponent(name)}`);
export const putProject = (p: Project) => api<ProjectView>("PUT", `/api/projects/${encodeURIComponent(p.metadata.name)}`, p);
export const deleteProject = (name: string) => api<void>("DELETE", `/api/projects/${encodeURIComponent(name)}`);
export type AccessPerson = { username: string; roles: Record<string, string>; via: Record<string, string> };
export type Assistant = { name: string; description?: string; redirectUris: string[]; addedBy?: string; addedAt?: string; clientId: string; mcpUrl: string };
export const getAssistants = () => api<{ assistants: Assistant[]; available: boolean }>("GET", "/api/assistants");
export const putAssistant = (a: { name: string; description?: string; redirectUris: string[] }) =>
  api<Assistant>("PUT", `/api/assistants/${encodeURIComponent(a.name)}`, a);
export const removeAssistant = (name: string) => api<void>("DELETE", `/api/assistants/${encodeURIComponent(name)}`);
export const getAccess = () => api<{ projects: string[]; people: AccessPerson[] }>("GET", "/api/access");

// --- A project's flow (from the catalog) --------------------------------------------

export type FlowCheck = { name: string; status: string; result: string; when?: number };
export type ColumnProfile = { nulls?: number; distinct?: number; min?: string | number; max?: string | number; mean?: number };
export type FlowColumn = { name: string; type: string; profile?: ColumnProfile };
export type FlowTable = { name: string; fqn: string; description?: string; columns: FlowColumn[]; checks: FlowCheck[]; external?: boolean; rows?: number; profiledAt?: number };
export type FlowEdge = { from: string; to: string; pipeline?: string; columns: { from: string[]; to: string }[] };
export type Flow = { tables: FlowTable[]; edges: FlowEdge[] };
export const getFlow = (name: string) => api<Flow>("GET", `/api/projects/${encodeURIComponent(name)}/flow`);

// --- A project's semantic layer (semanticd, through platformd) ----------------------

export type SemanticMetric = { name: string; description: string; type: string; dimensions: string[] };
export type SemanticModel = { project: string; yaml: string; metrics: SemanticMetric[]; tables: string[]; tests?: { name: string }[]; error?: string };
export type MetricQuery = { metrics: string[]; group_by?: string[]; where?: string[]; order_by?: string[]; limit?: number; explain?: boolean };
export type SemanticTest = { name: string; metrics: string[]; group_by: string[]; passed: boolean; why?: string; columns?: string[]; rows?: unknown[][] };
export const runSemanticTests = (name: string) =>
  api<{ tests: SemanticTest[]; passed: number; failed: number }>("POST", `/api/projects/${encodeURIComponent(name)}/semantic/tests`, {});
export type MetricAnswer = { project: string; sql: string; columns?: string[]; rows?: unknown[][] };
export const getSemantic = (name: string) => api<SemanticModel>("GET", `/api/projects/${encodeURIComponent(name)}/semantic`);
export const putSemantic = (name: string, yaml: string) => api<SemanticModel>("PUT", `/api/projects/${encodeURIComponent(name)}/semantic`, { yaml });
export const queryMetrics = (name: string, q: MetricQuery) => api<MetricAnswer>("POST", `/api/projects/${encodeURIComponent(name)}/semantic/query`, q);

// --- SQL, as the person asking (semanticd runs it in Trino with their token) --------

export type SQLAnswer = { columns: string[]; rows: unknown[][]; seconds?: number };
export const runSQL = (name: string, sql: string, limit?: number) =>
  api<SQLAnswer>("POST", `/api/projects/${encodeURIComponent(name)}/sql`, { sql, limit });

// --- A project's pipelines: the DAGs in its repo, and the platform's own tagged project:<name>

export type PipelineRun = { id: string; state: string; started: string; ended: string; requestedBy?: string; type: string; commit?: string };
export type PipelineTask = { id: string; state: string; started: string; ended: string };
export type Pipeline = {
  id: string; description: string; paused: boolean; runs: PipelineRun[]; tasks: PipelineTask[];
  bundle: string; commit?: string; clashesWith?: string[]; hasImportErrors: boolean;
};
export type PipelinesRepo = { url: string; path?: string; ref?: string };
export type ImportError = { file: string; error: string };
export const getPipelines = (name: string) =>
  api<{ pipelines: Pipeline[]; canRun: boolean; repo?: PipelinesRepo; importErrors?: ImportError[]; airflowPath?: string }>("GET", `/api/projects/${encodeURIComponent(name)}/pipelines`);
export const startRun = (name: string, pipeline: string) =>
  api<{ run: string }>("POST", `/api/projects/${encodeURIComponent(name)}/pipelines/${encodeURIComponent(pipeline)}/runs`);

// Airflow's times: Go's zero time means "not yet".
export function when(t?: string): Date | undefined {
  if (!t || t.startsWith("0001-")) return undefined;
  const d = new Date(t);
  return isNaN(d.getTime()) ? undefined : d;
}
export function ago(d?: Date): string {
  if (!d) return "";
  const s = Math.round((Date.now() - d.getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return d.toLocaleDateString();
}
export function took(a?: Date, b?: Date): string {
  if (!a || !b) return "";
  const s = Math.max(0, Math.round((b.getTime() - a.getTime()) / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

// --- The platform's health: its services, and the alerts about them -------------------

export type HealthPod = { name: string; node?: string; phase: string; waiting?: string; event?: string };
export type HealthService = {
  name: string; label: string; features: string[]; state: "up" | "down" | "unknown";
  reason?: string; since: string; pods?: HealthPod[]; mutedUntil?: string;
};
export type HealthAlert = { title: string; severity: string; facts: [string, string][]; at: string };
export type HealthFinding = { key: string; title: string; detail?: string; severity: string; since: string };
export type Health = {
  checked: boolean; affected: string[];
  // administrators only:
  services?: HealthService[]; alerts?: HealthAlert[]; findings?: HealthFinding[];
  certificate?: { ends: string; error?: string };
  targets?: { teams: boolean; email?: string[]; error?: string };
  backups?: { configured: boolean; schedule?: string; lastSuccess: string; since: string };
};
export const getHealth = () => api<Health>("GET", "/api/health");
export const muteAlerts = (service: string, hours: number) =>
  api<{ mutedUntil: string }>("POST", "/api/health/mute", { service, hours });
export const testAlert = () => api<{ targets: Record<string, string> }>("POST", "/api/health/test");

// --- A project's alerts: what's wrong in it now, and where it's told ----------------

export type ProjectIssue = { key: string; kind: "pipeline" | "check" | "stale"; title: string; detail?: string; link: string; mutedUntil?: string };
export type ProjectAlerts = { checked: boolean; issues: ProjectIssue[]; notify?: { teams: boolean; slack?: boolean; email: string[] }; unknown?: string[] };
export const getProjectAlerts = (name: string) => api<ProjectAlerts>("GET", `/api/projects/${encodeURIComponent(name)}/alerts`);
export const muteProjectAlert = (name: string, key: string, hours: number) =>
  api<ProjectAlerts>("POST", `/api/projects/${encodeURIComponent(name)}/alerts/mute`, { key, hours });

// --- Table maintenance: compaction ---------------------------------------------------

export type TableStats = { dataFiles: number; bytes: number; rows: number; snapshots: number; oldest: string; lastChange: string; refs: number; deleteFiles: number };
export type TableRun = { table: string; started: string; ended: string; done: string[]; error?: string; before: TableStats; after: TableStats };
export type Maintenance = {
  enabled: boolean; at?: string; state?: "idle" | "queued" | "running"; paused?: boolean;
  keepHistory?: string; // ISO 8601, as P7D: how far back the data as it was goes
  tables: { name: string; now?: TableStats; lastRun?: TableRun }[];
};
export const getMaintenance = (name: string) => api<Maintenance>("GET", `/api/projects/${encodeURIComponent(name)}/maintenance`);
// A run's log: each task's last try, so a failed run says why in the words of what ran.
export const getRunLog = (name: string, pipeline: string, run: string) =>
  api<{ log: string }>("GET", `/api/projects/${encodeURIComponent(name)}/pipelines/${encodeURIComponent(pipeline)}/runs/${encodeURIComponent(run)}/log`);
export const runMaintenance = (name: string) => api<{ state: string }>("POST", `/api/projects/${encodeURIComponent(name)}/maintenance`);

// --- The audit log: what people did in the platform, and every access Trino allowed or refused

export type AuditEvent = {
  time: string; user: string; action: string; resource: string; allowed: boolean;
  detail?: string; source: string; policy?: number; ip?: string; reason?: string;
};
export type AuditFilter = { user?: string; result?: "" | "allowed" | "denied"; source?: string; q?: string; before?: string; since?: string };
const auditParams = (f: AuditFilter) =>
  new URLSearchParams(Object.entries(f).filter(([, v]) => v) as [string, string][]).toString();
export const getProjectAudit = (name: string, f: AuditFilter) =>
  api<{ enabled: boolean; events: AuditEvent[]; more?: boolean }>("GET", `/api/projects/${encodeURIComponent(name)}/audit?${auditParams(f)}`);
export const getAudit = (f: AuditFilter & { project?: string }) =>
  api<{ enabled: boolean; events: AuditEvent[]; more?: boolean }>("GET", `/api/audit?${auditParams(f)}`);
export const auditCSV = (f: AuditFilter & { project?: string }) => `/api/audit?${auditParams({ ...f, format: "csv" } as AuditFilter)}`;

// --- The data as it was: a project's tags, a table's history, a moment's commit ---------

export type Moment = { commit: string; time?: string; label: string };
export const asOf = (name: string, at: string) => api<Moment>("GET", `/api/projects/${encodeURIComponent(name)}/asof?at=${encodeURIComponent(at)}`);
export type Tag = { name: string; commit: string; keepUntil: string; note?: string; by?: string; nessie: string; inPlace: boolean };
// --- A project's deliveries (9.1), imports' reset (8.2), copies' last changes (8.1)
export type DeliveryRun = { date: string; rows: number; target: string; recorded: string; summary: string;
  files: { name: string; rows: number; bytes: number; sha256: string }[]; approval?: { by?: string[]; protection?: string[] } };
export type DeliveryView = {
  name: string; table?: string; query?: string; format: string; gzip?: boolean; files: string; splitBy?: string; schedule: string;
  target: string; pgp?: string; changedBy?: string; approval?: { by: string[]; protection?: string[]; at: string; done?: boolean };
  approved: boolean; personal: string[]; needs: string[]; runs: DeliveryRun[];
};
export const getDeliveries = (name: string) => api<{ deliveries: DeliveryView[]; canSearch: boolean }>("GET", `/api/projects/${encodeURIComponent(name)}/deliveries`);
export type SearchHit = { delivery: string; date: string; sent: string; target: string; approval?: { by?: string[]; protection?: string[] }; rows: number; error?: string };
export const searchDeliveries = (name: string, person: string) =>
  api<{ runs: SearchHit[]; checked: number; notKept: number }>("GET", `/api/projects/${encodeURIComponent(name)}/deliveries/search?person=${encodeURIComponent(person)}`);
export const approveDelivery = (name: string, d: string) =>
  api<{ approved: boolean; needs: string[] }>("POST", `/api/projects/${encodeURIComponent(name)}/deliveries/${encodeURIComponent(d)}/approve`);
export const resetImport = (name: string, i: string) =>
  api<{ run: string }>("POST", `/api/projects/${encodeURIComponent(name)}/imports/${encodeURIComponent(i)}/reset`);
// --- The company's terms (10.1): the shared semantic models' entities, metrics and dimensions, in the business's words
export type Term = {
  name: string; label: string; kind: "entity" | "metric" | "dimension" | ""; definition: string; owner: string; synonyms: string[];
  project: string; tables: string[]; metrics: string[]; usedBy: string[];
};
export const getTerms = () => api<{ terms: Term[] }>("GET", "/api/terms");

// --- Data products (10.3): tables projects publish, read in place by the projects their producers approve
export type Product = {
  project: string; name: string; table: string; description?: string; owner: string; version: string; terms?: string[];
  contract?: { columns?: string[]; within?: string; checks?: boolean }; consumers?: string[];
  certification?: { by: string; at: string }; certified: boolean; canApprove: boolean;
  requests: { consumer: string; by: string; at: string; why?: string }[];
};
const prod = (p: Product | { project: string; name: string }) => `/api/products/${encodeURIComponent(p.project)}/${encodeURIComponent(p.name)}`;
export const getProducts = () => api<{ products: Product[]; canCertify: boolean }>("GET", "/api/products");
export const askForProduct = (p: Product, consumer: string, why: string) => api<void>("POST", `${prod(p)}/requests`, { consumer, why });
export const answerRequest = (p: Product, consumer: string, approve: boolean) =>
  api<void>("POST", `${prod(p)}/requests/${encodeURIComponent(consumer)}/${approve ? "approve" : "decline"}`);
export const removeConsumer = (p: Product, consumer: string) => api<void>("DELETE", `${prod(p)}/consumers/${encodeURIComponent(consumer)}`);
export const certify = (p: Product, on: boolean) => api<void>(on ? "POST" : "DELETE", `${prod(p)}/certification`);

// --- A project's connections (7.6): credentials given once, never shown again
export type ConnectionField = { key: string; label: string; secret?: boolean; required?: boolean; multiline?: boolean };
export type ConnectionKind = { kind: string; label: string; for: string; fields: ConnectionField[]; oneOf?: string[] };
export type Connection = {
  name: string; kind?: string; addedBy?: string; addedAt?: string; changedBy?: string; changedAt?: string;
  values: Record<string, string>; set: string[]; usedBy: string[];
};
const conns = (name: string) => `/api/projects/${encodeURIComponent(name)}/connections`;
export const getConnections = (name: string) => api<{ connections: Connection[]; kinds: ConnectionKind[]; canEdit: boolean }>("GET", conns(name));
export const addConnection = (name: string, c: { name: string; kind: string; values: Record<string, string> }) => api<Connection>("POST", conns(name), c);
export const changeConnection = (name: string, c: string, kind: string | undefined, values: Record<string, string>) =>
  api<Connection>("PUT", `${conns(name)}/${encodeURIComponent(c)}`, { kind, values });
export const removeConnection = (name: string, c: string) => api<void>("DELETE", `${conns(name)}/${encodeURIComponent(c)}`);

// --- A project's releases (7.4): plans of its repo's storscale/ at a ref, applied by someone else
export type Plan = {
  id: string; ref: string; commit?: string; target: string; by: string; at: string;
  state: "fetching" | "ready" | "failed" | "applied"; error?: string; changes: string[]; appliedBy?: string; appliedAt?: string;
};
export const getPlans = (name: string) =>
  api<{ plans: Plan[]; release?: Plan; target: string; repo: boolean; canPlan: boolean; canApply: boolean }>("GET", `/api/projects/${encodeURIComponent(name)}/plans`);
export const makePlan = (name: string, ref: string) => api<Plan>("POST", `/api/projects/${encodeURIComponent(name)}/plans`, { ref });
export const applyPlan = (name: string, id: string) =>
  api<Plan>("POST", `/api/projects/${encodeURIComponent(name)}/plans/${encodeURIComponent(id)}/apply`);
export type CopyTable = { source: string; from: string; table: string; lastChange?: string; error?: string };
export const getCopies = (name: string) => api<{ copies: CopyTable[] }>("GET", `/api/projects/${encodeURIComponent(name)}/copies`);

export const getTags = (name: string) => api<{ tags: Tag[]; keepHistory: string }>("GET", `/api/projects/${encodeURIComponent(name)}/tags`);
export const makeTag = (name: string, tag: { name: string; at: string; keep: string; note: string }) =>
  api<Tag>("POST", `/api/projects/${encodeURIComponent(name)}/tags`, tag);
export const removeTag = (name: string, tag: string) => api<void>("DELETE", `/api/projects/${encodeURIComponent(name)}/tags/${encodeURIComponent(tag)}`);
export type Change = {
  hash: string; time: string; message: string; author?: string; keys?: string[];
  stats?: TableStats & { operation?: string; addedRows: number; deletedRows: number; addedFiles: number; deletedFiles: number };
  tags?: string[]; who?: string; kept: boolean; query: string;
};
export const getHistory = (name: string, table: string) =>
  api<{ table: string; changes: Change[]; keepHistory: string }>("GET", `/api/projects/${encodeURIComponent(name)}/history?table=${encodeURIComponent(table)}`);

// atCommit names the project's tables in a query as they were at a commit
// ("orders#<commit>": Nessie reads it so), or as they are (commit "").
export function atCommit(sql: string, tables: string[], commit: string): string {
  return sql.replace(/\b(from|join)(\s+)((?:"?[a-z_][a-z0-9_]*"?\.){0,2})"?([a-z_][a-z0-9_]*)(?:#[0-9a-f]+)?"?/gi,
    (whole, kw: string, sp: string, prefix: string, t: string) =>
      tables.includes(t.toLowerCase()) ? `${kw}${sp}${prefix}${commit ? `"${t.toLowerCase()}#${commit}"` : t.toLowerCase()}` : whole);
}

// --- Flows: pipelines built on the canvas (use cases 11.1 and 11.2) ------------------
// A flow's steps are dbt models in the project's repo; the canvas reads and
// writes them through platformd, which compiles each step's columns and
// SQL, runs previews as the person, and saves as a merge request.

export type StepColumn = { name: string; type?: string };
export type FlowColumnSetting = { name: string; as?: string; type?: string; expression?: string };
export type FlowProcessor = { column: string; do: string; args?: string[]; as?: string };
export type FlowProcessorDef = { name: string; label: string; group: string; args: { name: string; kind: string; optional?: boolean }[]; type?: string };
export type FlowStep = {
  id: string; kind: string; description?: string; inputs?: string[];
  table?: string; catalog?: string; namespace?: string;
  columns?: FlowColumnSetting[]; where?: string;
  join?: string; on?: { left: string; right: string }[]; distinct?: boolean;
  by?: string[]; order?: { column: string; desc?: boolean }[]; partition?: string[];
  aggregates?: { function: string; column?: string; as?: string }[];
  column?: string; values?: string[]; value?: string; function?: string;
  unpivot?: string[]; name?: string;
  windows?: { function: string; column?: string; as: string; offset?: number }[];
  processors?: FlowProcessor[];
  prompt?: string; mode?: string; bank?: string; limit?: number;
  checks?: { column: string; test: string; values?: string[] }[];
  metrics?: string[]; sql?: string; x: number; y: number;
};
export type CanvasFlow = { name: string; schedule?: string; steps: FlowStep[] };
export type FlowStepResult = { model: string; columns: StepColumn[] | null; sql: string; error?: string; unknown?: boolean };
export type FlowView = {
  flow: CanvasFlow; order: string[]; steps: Record<string, FlowStepResult>; errors: string[];
  branch?: string; mergeRequest?: string; dbtPath?: string;
  processors?: FlowProcessorDef[]; // what a prepare step offers
};
export type FlowItem = { name: string; branch?: string; mergeRequest?: string };
export type FlowPreview = SQLAnswer & { sql: string; types?: string[]; sampled?: string[]; cost?: number; batches?: number; unread?: number; notes?: string[] };
export type FlowSaved = { branch: string; mergeRequest: string; new: boolean; files: string[]; dag: string };

const flowsPath = (name: string) => `/api/projects/${encodeURIComponent(name)}/flows`;
export const getFlows = (name: string) => api<{ flows: FlowItem[]; why?: string; dbtPath?: string }>("GET", flowsPath(name));
export const getFlowView = (name: string, flow: string) => api<FlowView>("GET", `${flowsPath(name)}/${encodeURIComponent(flow)}`);
export const compileFlow = (name: string, flow: CanvasFlow) => api<FlowView>("POST", `${flowsPath(name)}/compile`, { flow });
export const previewFlow = (name: string, flow: CanvasFlow, step: string, limit = 100) =>
  api<FlowPreview>("POST", `${flowsPath(name)}/preview`, { flow, step, limit });
export const saveFlow = (name: string, flow: CanvasFlow) => api<FlowSaved>("PUT", `${flowsPath(name)}/${encodeURIComponent(flow.name)}`, { flow });

// --- Uploads: files in the project's landing area (R11.3), put there as the person ---
export type Upload = { name: string; size: number; at: string; format?: string };
export type UploadPreview = { columns: { name: string; type: string }[]; rows: string[][]; partial?: boolean; note?: string };
export const getUploads = (name: string) => api<{ uploads: Upload[]; prefix: string; bucket: string }>("GET", `/api/projects/${encodeURIComponent(name)}/uploads`);
export const getUploadPreview = (name: string, file: string) =>
  api<{ name: string; preview: UploadPreview; table: string }>("GET", `/api/projects/${encodeURIComponent(name)}/uploads/${encodeURIComponent(file)}`);
export const removeUpload = (name: string, file: string) => api<void>("DELETE", `/api/projects/${encodeURIComponent(name)}/uploads/${encodeURIComponent(file)}`);
export async function uploadFile(name: string, file: File): Promise<{ upload: Upload; preview: UploadPreview; table: string }> {
  const form = new FormData();
  form.append("file", file, file.name);
  const res = await fetch(`/api/projects/${encodeURIComponent(name)}/uploads`, { method: "POST", headers: { "X-Platform-Request": "1" }, body: form });
  const json = await res.json().catch(() => ({}));
  if (res.status === 401) { signIn(); throw new ApiError(401, "signed out"); }
  if (!res.ok) throw new ApiError(res.status, json.error ?? `HTTP ${res.status}`);
  return json;
}

// --- Classification: where personal data is (use case 12.5) ---------------------------
export type ProtectedColumn = { table: string; column: string; tag: string; state: "suggested" | "confirmed"; declared?: boolean; automated?: boolean };
export const getClassification = (name: string) => api<{ columns: ProtectedColumn[]; at: number; tags: string[] }>("GET", `/api/projects/${encodeURIComponent(name)}/classification`);
export const classify = (name: string, table: string, column: string, tag: string, action: "confirm" | "remove") =>
  api<{ state: string }>("POST", `/api/projects/${encodeURIComponent(name)}/classification`, { table, column, tag, action });

// --- Agents of the project's own (use case 12.2) -------------------------------------
export type AgentView = {
  name: string; description?: string; owner: string; tools?: string[]; schedule?: string; notebook?: string; budget?: number;
  registeredBy?: string; registeredAt?: string; approvedBy?: string; approvedAt?: string;
  clientId: string; connection: string; approved: boolean; mcpUrl: string; dag?: string;
};
export const getAgents = (name: string) => api<{ agents: AgentView[]; tools: string[]; grants?: string[]; canEdit: boolean }>("GET", `/api/projects/${encodeURIComponent(name)}/agents`);
export const putAgent = (name: string, a: { name: string; description?: string; owner: string; tools?: string[]; schedule?: string; notebook?: string; prompt?: string; budget?: number }) =>
  api<AgentView[]>("PUT", `/api/projects/${encodeURIComponent(name)}/agents/${encodeURIComponent(a.name)}`, { description: a.description, owner: a.owner, tools: a.tools, schedule: a.schedule, notebook: a.notebook, prompt: a.prompt, budget: a.budget || undefined });
export type KnowledgeBank = { name: string; description?: string; tables?: { table: string; column: string; id: string }[]; files?: { prefix: string }[]; schedule?: string; table: string; chunks?: number };
export type KnowledgeHit = { source: string; ref: string; text: string; score: number };
const knowledgePath = (name: string) => `/api/projects/${encodeURIComponent(name)}/knowledge`;
export const getKnowledge = (name: string) =>
  api<{ banks: KnowledgeBank[]; canBuild: boolean; hasBudget?: boolean; hasFiles?: boolean; hasPipelines?: boolean }>("GET", knowledgePath(name));
export type KnowledgeSpec = { description?: string; tables?: { table: string; column: string; id: string }[]; files?: { prefix: string }[]; schedule?: string };
export const putKnowledge = (name: string, bank: string, k: KnowledgeSpec) => api<KnowledgeSpec>("PUT", `${knowledgePath(name)}/${encodeURIComponent(bank)}`, k);
export const deleteKnowledge = (name: string, bank: string) => api<void>("DELETE", `${knowledgePath(name)}/${encodeURIComponent(bank)}`);
export const buildKnowledge = (name: string, bank: string) => api<{ table: string; chunks: number; files?: number; skipped?: number; cost: number }>("POST", `${knowledgePath(name)}/${encodeURIComponent(bank)}/build`, {});
export const searchKnowledge = (name: string, bank: string, query: string, k = 5) =>
  api<{ hits: KnowledgeHit[]; cost: number }>("POST", `${knowledgePath(name)}/${encodeURIComponent(bank)}/search`, { query, k });
export type LLMCall = { at: string; who: string; user: string; kind: string; model: string; tokens: number; cost: number; seconds: number; status: string };
export type LLMTotal = { calls: number; tokens: number; cost: number };
export const getLLMCalls = (name: string, days = 7) =>
  api<{ days: number; total: LLMTotal; calls: LLMCall[]; byUser: Record<string, LLMTotal>; byModel: Record<string, LLMTotal>; byWho: Record<string, LLMTotal>; byDay: Record<string, LLMTotal>; errors?: string[] }>(
    "GET", `/api/projects/${encodeURIComponent(name)}/llm/calls?days=${days}`);
export type LLMUse = { who: string; budget: number; spend: number; key: boolean; error?: string; share?: number };
export const getLLM = (name: string) => api<{ gateway: string; uses: LLMUse[] }>("GET", `/api/projects/${encodeURIComponent(name)}/llm`);
export const approveAgent = (name: string, agent: string) =>
  api<{ approved: boolean; by: string; clientId: string; secret?: string; mcpUrl: string }>("POST", `/api/projects/${encodeURIComponent(name)}/agents/${encodeURIComponent(agent)}/approve`, {});
export const removeAgent = (name: string, agent: string) => api<void>("DELETE", `/api/projects/${encodeURIComponent(name)}/agents/${encodeURIComponent(agent)}`);

// --- Usage and cost per project (use case 13.1) ---------------------------------------
export type UsageShare = { queries: number; cpuSeconds: number };
export type UsageMonth = {
  project: string; month: string; currency: string; budget?: number; share?: number; avgGB: number; llmSpend: number;
  cost: { trino: number; storage: number; llm: number; total: number };
  trino: { queries: number; cpuSeconds: number; readBytes: number; failed: number; byUser?: Record<string, UsageShare>; byDay?: Record<string, UsageShare>;
    top?: { id: string; user: string; at: string; cpuSeconds: number; readBytes: number; sql: string }[] };
  storage?: { tables: Record<string, number>; bucket: number; llmSpend: number }; storageAt?: string;
};
export type Rates = { currency: string; cpuHour: number; storageGBMonth: number };
export const getProjectUsage = (name: string, month?: string) =>
  api<{ usage: UsageMonth; before: { month: string; cost: UsageMonth["cost"] }; rates: Rates }>("GET", `/api/projects/${encodeURIComponent(name)}/usage${month ? `?month=${month}` : ""}`);
export const getUsage = (month?: string) => api<{ month: string; projects: (UsageMonth & { before: number })[]; rates: Rates }>("GET", `/api/usage${month ? `?month=${month}` : ""}`);

// --- Governance across projects (use case 12.9) ---------------------------------------
export type Governance = {
  summary: string;
  pending: { project: string; kind: "agent" | "delivery" | "access"; name: string; waits: string; since?: string; link: string }[];
  agents: { project: string; name: string; owner: string; approved: boolean; tools: string[]; runs?: string; budget: number; spend: number; lastRun?: { state: string; started: string } }[];
  rules: { project: string; readers: string[]; tables?: string[]; rowFilters?: string[]; masks?: string[]; people?: string[]; tagged?: string[] }[];
  tagRules: { tag: string; mask: string; except: string[] }[];
  suggestedApplied: boolean;
  products: { project: string; name: string; owner: string; certified: boolean; consumers: string[] }[];
  erasures: { project: string; id: string; at: string; dueBy: string; by: string; errors: number }[];
};
export const getGovernance = () => api<Governance>("GET", "/api/governance");

// --- Erasure of a person's data (use case 6.9) ------------------------------------------
export type Erasure = {
  id: string; project: string; personHash: string; requestedOn: string; dueBy: string; by: string; verifiedBy: string; at: string;
  tables: { table: string; column: string; rows: number }[]; deliveries: SearchHit[]; deliveriesChecked: number; deliveriesNotCheckable: number;
  historyKept?: string; agesOutBy?: string; closes?: string[]; errors?: Record<string, string>;
};
export const getErasures = (name: string) => api<{ erasures: Erasure[]; deadlineDays: number; canErase: boolean }>("GET", `/api/projects/${encodeURIComponent(name)}/erasures`);
export const erasePerson = (name: string, person: string, requestedOn?: string, verifiedBy?: string) =>
  api<Erasure>("POST", `/api/projects/${encodeURIComponent(name)}/erasures`, { person, requestedOn: requestedOn || undefined, verifiedBy: verifiedBy || undefined });
