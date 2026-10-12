"""The platform's shell, in a real browser (Chromium): docker compose run --rm test-browser

  1. alice signs in to the platform once, with Keycloak's form;
  2. then opens notebooks, SQL, dashboards, pipelines and monitoring in the
     platform's frame, each signed in as alice, without another sign-in;
  3. the sales project in the platform's own pages: alice, a reader, sees its
     tables (a preview and her SQL showing only what Ranger lets her see), asks
     for a metric, sees its pipelines and finds a column with search, but can't
     change the project; bob, an administrator, sees its members, can change its
     metrics, starts a pipeline run, and sees who has access;
  4. carol (in neither group) signs in, and the platform refuses her;
  5. signing out of the platform ends the Keycloak session too.

Chromium resolves *.localhost names to its own machine, as every browser
does, so this listens on 127.0.0.1:STORSCALE_PORT and passes connections on
to the gateway: the browser uses the addresses a person's browser would.
"""
import os
import re
import socket
import sys
import threading
import time

from playwright.sync_api import sync_playwright

PORT = int(os.environ["STORSCALE_PORT"])
PLATFORM = f"http://{os.environ['STORSCALE_DOMAIN']}:{PORT}"
AUTH = f"{PLATFORM}/sso/"  # Keycloak, at the platform's /sso/
PASSWORD = os.environ["LAKEHOUSE_USER_PASSWORD"]

# Each tool, and where its frame should end up once alice is signed in to it.
TOOLS = {
    "notebooks": r"/notebooks/user/alice/lab",
    "sql": r"/dashboards/sqllab",
    "dashboards": r"/dashboards/dashboard/list",
    "pipelines": r"/pipelines/(\?.*)?$",
    "monitoring": r"/monitoring/(\?.*)?$",
}

failures = []


def check(name, ok, detail=""):
    print(f"{'ok  ' if ok else 'FAIL'} {name}{f'  ({detail})' if detail else ''}", flush=True)
    if not ok:
        failures.append(name)


def forward(port, host):
    """127.0.0.1:port -> host:port, for as long as this runs."""
    listener = socket.create_server(("127.0.0.1", port))

    def pipe(a, b):
        try:
            while data := a.recv(65536):
                b.sendall(data)
        except OSError:
            pass
        finally:
            for s in (a, b):
                try:
                    s.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass

    def serve():
        while True:
            client, _ = listener.accept()
            upstream = socket.create_connection((host, port))
            threading.Thread(target=pipe, args=(client, upstream), daemon=True).start()
            threading.Thread(target=pipe, args=(upstream, client), daemon=True).start()

    threading.Thread(target=serve, daemon=True).start()


def press(page, selector):
    """Click an element of the platform's own page through the page itself: with
    tools loaded in its frames, a headless browser on a busy machine can take
    longer than Playwright's click allows to confirm the element is clickable."""
    page.wait_for_selector(selector, timeout=60_000)
    page.eval_on_selector(selector, "e => e.click()")


def sign_in(page, user):
    """Keycloak's form, as a person fills it in."""
    page.wait_for_selector("#kc-form-login", timeout=60_000)
    page.fill("#username", user)
    page.fill("#password", PASSWORD)
    page.click("#kc-login")


def main():
    forward(PORT, "gateway")
    with sync_playwright() as p:
        engines = os.environ.get("SHELL_BROWSERS", "chromium webkit").split()
        for engine in engines:
            print(f"--- in {engine} ({'Safari' if engine == 'webkit' else 'Chrome, Edge'})", flush=True)
            # Switching between tools in one page holds several whole apps at
            # once: CI has the memory for it (SHELL_SWITCH=1); a laptop's Docker,
            # running the platform too, may not.
            run(getattr(p, engine).launch(), switch=os.environ.get("SHELL_SWITCH") == "1" and engine != "webkit",
                start_run=engine == engines[0])  # one pipeline run is enough

    print()
    if failures:
        sys.exit(f"{len(failures)} of the checks failed")
    print("all checks passed")


def run(browser, switch=True, start_run=True):
    """The checks, in one browser. switch: open the tools one after another in
    the same page, as a person switching between them would; otherwise each in
    a page of its own (lighter: on a machine short of memory, a headless browser
    stalls with Grafana loading beside Airflow)."""
    # 1. alice signs in, once.
    ctx = browser.new_context(viewport={"width": 1400, "height": 900})
    sign_ins = []
    ctx.on("request", lambda r: sign_ins.append(r.url) if r.method == "POST" and r.url.startswith(AUTH)
           and "/login-actions/authenticate" in r.url else None)
    page = ctx.new_page()
    page.goto(PLATFORM)
    sign_in(page, "alice")
    page.wait_for_selector("[data-testid=greeting]", timeout=60_000)
    check("alice signs in to the platform with Keycloak", "Alice" in page.inner_text("[data-testid=greeting]"),
          page.inner_text("[data-testid=greeting]"))
    names = [(a.text_content() or "").strip() for a in page.query_selector_all(".topbar [data-testid^=nav-]")]
    check("... and the Tools menu lists her tools", all(any(n.startswith(t) for n in names) for t in ("Notebooks", "SQL", "Dashboards", "Pipelines", "Monitoring"))
          and not any(n.startswith("Access policies") for n in names), ", ".join(names))
    page.wait_for_selector("[data-testid=project-sales]", timeout=60_000)
    card = page.inner_text("[data-testid=project-sales]")
    check("... and Home shows her project, sales, where she's a reader", "Reader" in card, " ".join(card.split())[:100])

    # 2. Each tool, in the platform's frame, without another sign-in.
    for tool, where in TOOLS.items():
        if switch:
            press(page, "[data-testid=tools-menu]")
            press(page, f"[data-testid=nav-{tool}]")
        else:
            page.goto(f"{PLATFORM}/tools/{tool}")
        frame, deadline = None, time.time() + 120
        while time.time() < deadline:
            try:  # the app may swap the element while it renders; look again
                el = page.query_selector(f"[data-testid=frame-{tool}]")
                frame = el.content_frame() if el else None
            except Exception:  # noqa: BLE001
                frame = None
            if frame and re.search(where, frame.url):
                break
            time.sleep(1)
        url = frame.url if frame and not frame.is_detached() else "no frame"
        shown = frame is not None and re.search(where, url) is not None
        if shown:
            # Its document, not every image and script on it: "load" can wait
            # on one slow asset (WebKit, on Superset's dashboard list, timed out).
            try:
                frame.wait_for_load_state("domcontentloaded", timeout=120_000)
            except Exception as e:  # noqa: BLE001 (a failed check, not the end of the suite)
                shown, url = False, f"{url}: didn't load ({type(e).__name__})"
        on_keycloak = frame is not None and frame.url.startswith(AUTH)
        check(f"{tool} opens in the platform, signed in as alice", shown and not on_keycloak, url)
    check("one sign-in for all of them", len(sign_ins) == 1, f"{len(sign_ins)} Keycloak sign-in(s)")
    catalog = page.get_attribute("[data-testid=nav-catalog]", "href") or ""
    check("the catalog opens in a tab of its own, through the platform's launcher",
          catalog.endswith("/_storscale/launch.html") and page.get_attribute("[data-testid=nav-catalog]", "target") == "_blank", catalog)

    # 3. The sales project, as alice (a reader), in the platform's own pages.
    page.goto(f"{PLATFORM}/p/sales")
    page.wait_for_selector("[data-testid=grants]", timeout=60_000)
    tabs = [t.inner_text() for t in page.query_selector_all(".tabs a")]
    check("alice opens sales: its tabs", all(t in tabs for t in ("Overview", "Data", "Metrics", "SQL", "Pipelines", "Access")), ", ".join(tabs))
    check("... she's a reader there, with no administration", page.inner_text("[data-testid=role]") == "Reader"
          and page.query_selector("[data-testid=edit-project]") is None and page.query_selector("[data-testid=nav-admin]") is None, "")
    page.goto(f"{PLATFORM}/p/sales/data")
    page.wait_for_selector("[data-testid=table-orders]", timeout=120_000)
    names = [e.get_attribute("data-testid")[6:] for e in page.query_selector_all("[data-testid=tables] [data-testid^=table-]")]
    check("... Data: the project's tables, from the catalog", {"orders", "orders_by_region", "payroll"} <= set(names), names)
    press(page, "[data-testid=table-orders]")
    page.wait_for_selector("[data-testid=columns]", timeout=30_000)
    columns = page.inner_text("[data-testid=columns]")
    limits = page.inner_text("[data-testid=limits]") if page.query_selector("[data-testid=limits]") else ""
    check("... with column types, and what a reader doesn't see", "card_number" in columns and "varchar" in columns
          and "last 4 only" in columns and "region = 'EU'" in limits, " ".join(limits.split())[:120])
    press(page, "[data-testid=data-tab-preview]")
    page.wait_for_selector("[data-testid=preview]", timeout=120_000)
    rows = preview_rows(page, "preview")
    check("... a preview of orders, as alice: EU rows only, card numbers masked",
          rows and all(r.get("region") == "EU" for r in rows) and all(not r.get("card_number") or r["card_number"][:-4].strip("x*X") == "" for r in rows),
          str(rows[:2]))
    page.goto(f"{PLATFORM}/p/sales/sql")
    page.wait_for_selector("[data-testid=sql]", timeout=60_000)
    page.fill("[data-testid=sql]", "SELECT region, count(*) AS n FROM orders GROUP BY region")
    press(page, "[data-testid=run-sql]")
    page.wait_for_selector("[data-testid=sql-result]", timeout=120_000)
    rows = preview_rows(page, "sql-result")
    check("... SQL: her query runs as her (Ranger's row filter applies)", rows and {r["region"] for r in rows} == {"EU"}, str(rows))
    page.goto(f"{PLATFORM}/p/sales/metrics")
    press(page, "[data-testid=metric-revenue]")
    press(page, "[data-testid=ask]")
    page.wait_for_selector("[data-testid=answer]", timeout=120_000)
    check("... Metrics: she asks for revenue, and gets an answer", "revenue" in page.inner_text("[data-testid=answer]"),
          " ".join(page.inner_text("[data-testid=answer]").split())[:80])
    check("... whose definitions she can read but not change", page.query_selector("[data-testid=save-semantic]") is None, "")
    page.goto(f"{PLATFORM}/p/sales/pipelines")
    page.wait_for_selector("[data-testid=pipeline-sales_ingest]", timeout=60_000)
    check("... Pipelines: sales_ingest and its runs, which she can't start", page.query_selector("[data-testid=run-now]") is None, "")
    page.goto(f"{PLATFORM}/admin")
    page.wait_for_selector(".page .empty", timeout=30_000)
    check("... and Administration isn't hers", page.query_selector("[data-testid=new-project]") is None, "")
    page.goto(f"{PLATFORM}/p/sales")
    page.wait_for_selector("[data-testid=search]", timeout=30_000)
    page.click("[data-testid=search]")
    page.keyboard.type("card")
    page.wait_for_selector("[data-testid=search-results] [role=option]", timeout=60_000)
    hits = page.inner_text("[data-testid=search-results]")
    check("... search finds the project's columns", "orders.card_number" in hits, " ".join(hits.split())[:80])

    # 5 (before carol, who has a context of her own). Signing out.
    page.goto(PLATFORM)
    press(page, "[data-testid=whoami]")
    press(page, "[data-testid=sign-out]")
    page.wait_for_selector("#kc-form-login", timeout=60_000)
    check("signing out ends the Keycloak session: the next visit asks for a password",
          page.url.startswith(AUTH), page.url.split("?")[0])
    ctx.close()

    # 3. The sales project, as bob (an administrator, and an editor of sales).
    ctx = browser.new_context(viewport={"width": 1400, "height": 900})
    page = ctx.new_page()
    page.goto(f"{PLATFORM}/projects/sales")  # an address from before the redesign
    sign_in(page, "bob")
    page.wait_for_selector("[data-testid=members]", timeout=60_000)
    members = page.inner_text("[data-testid=members]")
    check("bob opens the sales project and sees its members", page.url.endswith("/p/sales") and "alice" in members
          and "reader" in members and "editor" in members, " ".join(members.split())[:120])
    check("... with what each role gets", "Read orders in iceberg.sales" in page.inner_text("[data-testid=grants]"), "")
    page.goto(f"{PLATFORM}/p/sales/metrics")
    press(page, "[data-testid=definitions-toggle]")
    page.wait_for_selector("[data-testid=semantic-yaml]", timeout=60_000)
    metrics = page.inner_text("[data-testid=metrics]")
    check("... and its metrics, whose definitions bob can change", "revenue" in metrics and "order_count" in metrics
          and page.get_attribute("[data-testid=semantic-yaml]", "readonly") is None, " ".join(metrics.split())[:80])
    page.goto(f"{PLATFORM}/p/sales/pipelines")
    page.wait_for_selector("[data-testid=pipeline-sales_ingest]", timeout=60_000)
    check("... and can start its pipelines", page.query_selector("[data-testid=run-now]") is not None, "")
    if start_run:
        press(page, "[data-testid=run-now]")
        page.wait_for_selector("[data-testid=run-started]", timeout=60_000)
        started = page.inner_text("[data-testid=run-started]")
        # Wait for it to end: the other suites run the pipeline too, and two runs
        # loading into Trino at once is more than a laptop's Docker holds.
        state, deadline = "", time.time() + 600
        while time.time() < deadline and state not in ("success", "failed"):
            time.sleep(5)
            state = page.evaluate("""() => fetch('/api/projects/sales/pipelines', {headers: {'X-Platform-Request': '1'}})
                .then(r => r.json()).then(b => b.pipelines.find(p => p.id === 'sales_ingest').runs[0].state)""")
        check("... and starts a run of sales_ingest, which runs", "manual" in started and state == "success", f"{started}; {state}")
    page.goto(f"{PLATFORM}/admin")
    page.wait_for_selector("[data-testid=project-sales]", timeout=30_000)
    press(page, "[data-testid=admin-access]")
    page.wait_for_selector("[data-testid=access]", timeout=30_000)
    access = page.inner_text("[data-testid=access]")
    check("... and Administration: who has which role", "alice" in access and "reader" in access, " ".join(access.split())[:120])
    ctx.close()

    # 4. carol: signed in, and refused.
    ctx = browser.new_context()
    page = ctx.new_page()
    page.goto(PLATFORM)
    sign_in(page, "carol")
    page.wait_for_selector("[data-testid=refused]", timeout=60_000)
    text = page.inner_text("[data-testid=refused]")
    check("people in neither group are refused", "No access" in text and page.query_selector(".topbar") is None,
          text.splitlines()[0])
    ctx.close()
    browser.close()


def preview_rows(page, testid):
    """A results table, as a list of {column: value}."""
    return page.eval_on_selector(f"[data-testid={testid}]", """t => {
        const cols = [...t.querySelectorAll('thead th')].map(th => th.innerText.trim());
        return [...t.querySelectorAll('tbody tr')].map(tr => Object.fromEntries([...tr.children].map((td, i) => [cols[i], td.innerText.trim()])));
    }""")


if __name__ == "__main__":
    main()
