// Schedule a notebook in a StorScale project (use case 7.7): a button on the
// notebook's toolbar, an item on a notebook's menu in the file browser, and a
// command in the palette. It asks which project and when, and the notebook
// server's extension (storscale_schedule) sends the notebook to the platform
// as the person, which commits it to the project's repo with a pipeline that
// runs it, as a merge request.
import { Dialog, ICommandPalette, showDialog, showErrorMessage } from "@jupyterlab/apputils";
import { IFileBrowserFactory } from "@jupyterlab/filebrowser";
import { ServerConnection } from "@jupyterlab/services";
import { Widget } from "@lumino/widgets";

const COMMAND = "storscale:schedule";

async function call(method, path, body) {
  const settings = ServerConnection.makeSettings();
  const init = { method };
  if (body) {
    init.body = JSON.stringify(body);
    init.headers = { "Content-Type": "application/json" };
  }
  const res = await ServerConnection.makeRequest(settings.baseUrl + "storscale/" + path, init, settings);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || data.message || `HTTP ${res.status}`);
  return data;
}

function el(tag, props, ...children) {
  const n = document.createElement(tag);
  Object.assign(n, props);
  for (const c of children) n.append(c);
  return n;
}

// The dialog's form: the project, and when (cron, UTC).
function form(projects, path) {
  const select = el("select", { name: "project", className: "jp-mod-styled" });
  for (const p of projects) {
    select.append(el("option", { value: p.name, disabled: !!p.why, textContent: p.why ? `${p.name} (${p.why})` : p.name }));
  }
  const usable = projects.find((p) => !p.why);
  if (usable) select.value = usable.name;
  const cron = el("input", { name: "schedule", value: "0 5 * * *", className: "jp-mod-styled", style: "font-family: var(--jp-code-font-family)" });
  const node = el("div", { style: "display: grid; gap: 8px; min-width: 360px" },
    el("div", { textContent: `${path.split("/").pop()} runs in the project's Airflow, as its pipelines. The platform opens a merge request in its repo with the notebook and the pipeline that runs it.` }),
    el("label", {}, "Project ", select),
    el("label", {}, "When (cron, UTC) ", cron),
    el("div", { className: "jp-mod-styled", style: "opacity: .7", textContent: "0 5 * * * is every day at 05:00; 0 6 * * 1 is Mondays at 06:00." }));
  const w = new Widget({ node });
  w.getValue = () => ({ project: select.value, schedule: cron.value.trim() });
  return w;
}

async function schedule(path) {
  let projects;
  try {
    projects = (await call("GET", "projects")).projects;
  } catch (e) {
    return showErrorMessage("Schedule", e.message);
  }
  if (!projects.length) {
    return showErrorMessage("Schedule", "You aren't an editor of a project with a repo, where a notebook can be scheduled.");
  }
  const body = form(projects, path);
  const result = await showDialog({ title: "Schedule this notebook", body, buttons: [Dialog.cancelButton(), Dialog.okButton({ label: "Schedule" })] });
  if (!result.button.accept) return;
  const { project, schedule } = body.getValue();
  try {
    const done = await call("POST", "schedule", { path, project, schedule });
    const link = el("a", { href: done.mergeRequest, target: "_blank", rel: "noopener", textContent: done.mergeRequest, style: "text-decoration: underline" });
    await showDialog({
      title: "Scheduled, for review",
      body: new Widget({ node: el("div", {}, `The pipeline ${done.dag} is in a merge request: `, link, ". Once merged, it runs at ", schedule, " (UTC), and each run's notebook shows on the project's Pipelines tab.") }),
      buttons: [Dialog.okButton()],
    });
  } catch (e) {
    showErrorMessage("Schedule", e.message);
  }
}

export default {
  id: "storscale-schedule:plugin",
  description: "Schedule a notebook in a StorScale project.",
  autoStart: true,
  optional: [ICommandPalette, IFileBrowserFactory],
  activate: (app, palette, browsers) => {
    // The notebook chosen: the one selected in the file browser (its menu), or the one open.
    const target = (args) => {
      if (args.from === "browser" && browsers) {
        const item = browsers.tracker.currentWidget?.selectedItems().next();
        const model = item && !item.done ? item.value : item;
        return model && model.type === "notebook" ? model.path : null;
      }
      const ctx = app.shell.currentWidget?.context;
      return ctx && ctx.path.endsWith(".ipynb") ? ctx.path : null;
    };
    app.commands.addCommand(COMMAND, {
      label: "Schedule in a project…",
      caption: "Run this notebook on a schedule, as a pipeline of a project",
      isEnabled: (args) => !!target(args),
      execute: (args) => {
        const path = target(args);
        if (path) return schedule(path);
      },
    });
    palette?.addItem({ command: COMMAND, category: "Notebook Operations" });
    app.contextMenu.addItem({ command: COMMAND, args: { from: "browser" }, selector: '.jp-DirListing-item[data-file-type="notebook"]', rank: 20 });
    // A button on every notebook's toolbar.
    app.docRegistry.addWidgetExtension("Notebook", {
      createNew: (panel) => {
        const button = el("button", { className: "jp-ToolbarButtonComponent jp-mod-minimal jp-Button", title: "Schedule this notebook in a project" },
          el("span", { className: "jp-ToolbarButtonComponent-label", textContent: "Schedule" }));
        button.onclick = () => schedule(panel.context.path);
        const w = new Widget({ node: button });
        w.addClass("jp-ToolbarButton");
        panel.toolbar.insertItem(10, "storscale-schedule", w);
        return w;
      },
    });
  },
};
