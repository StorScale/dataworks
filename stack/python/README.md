# storscale

DataWorks' API, as Python (use case 14.3). Every call runs as whoever signed in, under their projects' rules, as the web app does.

```sh
pip install "git+https://github.com/StorScale/dataworks@v0.31.0#subdirectory=stack/python"
```

Python 3.10 or later; it needs only `requests`. Leave out `@v0.31.0` for the latest from `main`.

```python
import storscale

p = storscale.Platform("https://storscale.example.com")   # or STORSCALE_URL
p.me()                                                    # who's signed in, and what the platform has
p.projects()                                              # the projects you may see
p.sql("sales", "SELECT region, sum(amount) FROM orders GROUP BY 1")   # {"columns": [...], "rows": [...]}
p.pipelines("sales")                                      # its pipelines and their latest runs
run = p.start_run("sales", "flow_weekly")                 # a project's editors: the run's ID
p.wait_for_run("sales", "flow_weekly", run)               # "success" or "failed"
print(p.run_log("sales", "flow_weekly", run))
p.search("sales", "reviews", "the zip broke")             # a knowledge bank, by meaning
p.usage("sales")                                          # this month's usage and cost
p.call("get_projects_name_usage", name="sales", month="2026-09")   # any route, by its operation ID
```

Also by name: `project(name)`, `flows(project)`, `flow(project, name)`, `knowledge(project)` (the banks, and whether each is built), and `request(method, path, json=, params=)` for a path of your own. `call()` puts the route's path parameters in by name, the rest in the query, and `body=` as JSON.

**Signing in**, in this order:

- `token=`, or `STORSCALE_TOKEN`: a Keycloak access token of the realm `lakehouse` (used as it is, never renewed);
- `client_id=` and `client_secret=`, or `STORSCALE_CLIENT_ID` and `STORSCALE_CLIENT_SECRET`: a service account (an agent of a project's, say), by client credentials;
- `username=` and `password=`, or `STORSCALE_USERNAME` and `STORSCALE_PASSWORD`: a person, through the public client `storscale-agent`;
- otherwise, by a device code: it shows a link and a code (`show=`, default `print`), you open the link in your own browser and approve, signing in as you always do (Entra ID included), and it carries on as you. For a notebook, a pod or any terminal without a browser of its own; a renewal that Keycloak refuses (a long idle) asks for approval again.

A token it got itself is renewed before it expires. A refusal raises `storscale.Refused` (HTTP 401 or 403), and any other failure `storscale.Error`, each with the platform's reason, the status and the request ID (`.message`, `.status`, `.request_id`). A private CA: set `REQUESTS_CA_BUNDLE`.

The routes and what each does: the platform's **API reference** (the account menu), or `/api/openapi.json`; `p.operations()` lists them by operation ID, each with its method, path and summary.
