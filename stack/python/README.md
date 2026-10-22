# storscale

StorScale Platform's API, as Python (use case 14.3). Every call runs as whoever signed in, under their projects' rules, as the web app does.

```sh
pip install "git+https://github.com/StorScale/storscale-platform#subdirectory=stack/python"
```

```python
import storscale

p = storscale.Platform("https://storscale.example.com")   # or STORSCALE_URL
p.projects()                                              # the projects you may see
p.sql("sales", "SELECT region, sum(amount) FROM orders GROUP BY 1")
run = p.start_run("sales", "flow_weekly")
print(p.run_log("sales", "flow_weekly", run))
p.search("sales", "reviews", "the zip broke")             # a knowledge bank, by meaning
p.usage("sales")                                          # this month's usage and cost
p.call("get_projects_name_usage", name="sales", month="2026-09")   # any route, by its operation ID
```

**Signing in**, in this order:

- `token=`, or `STORSCALE_TOKEN`: a Keycloak access token of the realm `lakehouse`;
- `client_id=` and `client_secret=`, or `STORSCALE_CLIENT_ID` and `STORSCALE_CLIENT_SECRET`: a service account (an agent of a project's, say), by client credentials;
- `username=` and `password=`, or `STORSCALE_USERNAME` and `STORSCALE_PASSWORD`: a person, through the public client `storscale-agent`;
- otherwise, at a terminal, it asks for your name and password.

A token is renewed before it expires. A refusal raises `storscale.Refused` (403) or `storscale.Error`, with the platform's reason and its request ID. A private CA: set `REQUESTS_CA_BUNDLE`.

The routes and what each does: the platform's **API reference** (the account menu), or `/api/openapi.json`; `p.operations()` lists them by operation ID.
