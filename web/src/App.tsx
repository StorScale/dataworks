import { lazy, Suspense, useEffect, useState } from "react";
import { Navigate, Route, Routes, useLocation, useParams } from "react-router-dom";
import { session, Session, signIn, signOut, User } from "./api";
import { ProjectFrame, TopBar } from "./components";
import { PlatformContext, Spinner, usePlatform } from "./lib";
import Admin, { AdminAccess, AdminMonitoring, AdminProjects } from "./pages/Admin";
import AdminAssistants from "./pages/Assistants";
import { AdminHealth } from "./pages/Health";
import { AdminAudit, ProjectAudit } from "./pages/Audit";
import Tags from "./pages/Tags";
import Connections from "./pages/Connections";
import Imports from "./pages/Imports";
import Deliveries from "./pages/Deliveries";
import Releases from "./pages/Releases";
import Products from "./pages/Products";
import Terms from "./pages/Terms";
import Data from "./pages/Data";
import Home from "./pages/Home";
import Metrics from "./pages/Metrics";
import Overview from "./pages/Overview";
import Pipelines from "./pages/Pipelines";
const Flows = lazy(() => import("./pages/Flows"));
import Governance from "./pages/Governance";
import Usage, { ProjectUsage } from "./pages/Usage";
import Reference from "./pages/Reference";
import Apps, { AppPage } from "./pages/Apps";
import Models from "./pages/Models";
import Agents from "./pages/Agents"; // React Flow is loaded only when the canvas is
import ProjectAccess from "./pages/ProjectAccess";
import ProjectEditor from "./pages/ProjectEditor";
import SQL from "./pages/SQL";
import ToolFrames from "./pages/ToolFrames";

export default function App() {
  const [s, setS] = useState<Session | null | undefined>(undefined);
  const [error, setError] = useState<string>();
  useEffect(() => {
    session()
      .then((s) => (s ? setS(s) : signIn()))
      .catch((e) => setError(String(e)));
  }, []);

  if (error) return <div className="center"><div className="banner error">The platform isn't answering: {error}</div></div>;
  if (!s) return <div className="center"><Spinner /></div>;
  if (s.state === "refused") return <Refused user={s.user} error={s.error} />;
  const { state: _, ...platform } = s;
  return (
    <PlatformContext.Provider value={platform}>
      <Shell />
    </PlatformContext.Provider>
  );
}

// The tool shown in a frame at this address, if any: a project's notebooks and
// dashboards, monitoring in Administration, or any tool at /tools/:id.
function frameAt(path: string): string | undefined {
  return path.match(/^\/tools\/([^/]+)/)?.[1]
    ?? path.match(/^\/p\/[^/]+\/(notebooks|dashboards)\/?$/)?.[1]
    ?? (/^\/admin\/monitoring\/?$/.test(path) ? "monitoring" : undefined);
}

function Shell() {
  const { tools, admin } = usePlatform();
  const location = useLocation();
  const frame = frameAt(location.pathname);
  return (
    <div className={frame ? "app fill" : "app"}>
      <TopBar />
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/terms" element={<Terms />} />
        <Route path="/products" element={<Products />} />
        <Route path="/terms/:term" element={<Terms />} />
        <Route path="/p/:project" element={<ProjectFrame />}>
          <Route index element={<Overview />} />
          <Route path="data" element={<Data />} />
          <Route path="metrics" element={<Metrics />} />
          <Route path="sql" element={<SQL />} />
          <Route path="pipelines" element={<Pipelines />} />
          <Route path="flows" element={<Suspense fallback={<Spinner />}><Flows /></Suspense>} />
          <Route path="agents" element={<Agents />} />
          <Route path="apps" element={<Apps />} />
          <Route path="apps/:app" element={<AppPage />} />
          <Route path="models" element={<Models />} />
          <Route path="notebooks" element={null} />
          <Route path="dashboards" element={null} />
          <Route path="access" element={<ProjectAccess />} />
          <Route path="audit" element={<ProjectAudit />} />
          <Route path="usage" element={<ProjectUsage />} />
          <Route path="tags" element={<Tags />} />
          <Route path="deliveries" element={<Deliveries />} />
          <Route path="releases" element={<Releases />} />
          <Route path="connections" element={<Connections />} />
          <Route path="imports" element={<Imports />} />
          <Route path="edit" element={admin ? <ProjectEditor /> : <Navigate to=".." replace />} />
        </Route>
        <Route path="/admin" element={admin ? <Admin /> : <NotFound />}>
          <Route index element={<Navigate to="projects" replace />} />
          <Route path="projects" element={<AdminProjects />} />
          <Route path="projects/new" element={<ProjectEditor />} />
          <Route path="access" element={<AdminAccess />} />
          <Route path="health" element={<AdminHealth />} />
          <Route path="audit" element={<AdminAudit />} />
          <Route path="governance" element={<Governance />} />
          <Route path="usage" element={<Usage />} />
          <Route path="assistants" element={<AdminAssistants />} />
          <Route path="monitoring" element={<AdminMonitoring />} />
        </Route>
        <Route path="/governance" element={<Governance />} />
        <Route path="/usage" element={<Usage />} />
        <Route path="/reference" element={<Reference />} />
        <Route path="/tools/:id" element={null} />
        {/* Addresses from before the redesign. */}
        <Route path="/projects" element={<Navigate to={admin ? "/admin/projects" : "/"} replace />} />
        <Route path="/projects/new" element={<Navigate to="/admin/projects/new" replace />} />
        <Route path="/projects/:name/*" element={<OldProject />} />
        <Route path="/access" element={<Navigate to="/admin/access" replace />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
      <ToolFrames tools={tools.filter((t) => t.embedUrl)} active={frame} />
    </div>
  );
}

function OldProject() {
  const { name = "", "*": rest = "" } = useParams();
  const to = { flow: "data", semantic: "metrics", edit: "edit" }[rest.split("/")[0]] ?? "";
  return <Navigate to={`/p/${name}${to ? `/${to}` : ""}`} replace />;
}

function NotFound() {
  return <div className="page"><div className="empty">There's no page here. <a href="/">Go home</a>.</div></div>;
}

function Refused({ user, error }: { user: User; error: string }) {
  return (
    <div className="center">
      <div className="card" style={{ maxWidth: 480 }} data-testid="refused">
        <h1>No access</h1>
        <p>Signed in as <strong>{user.username}</strong>. {error}</p>
        <p className="muted">Ask an administrator to add you to a group in Keycloak, then sign in again.</p>
        <button className="btn" onClick={signOut}>Sign out</button>
      </div>
    </div>
  );
}
