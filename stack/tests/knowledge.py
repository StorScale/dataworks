"""Knowledge banks: docker compose run --rm test knowledge

A project of the suite's own, banked, with an LLM budget and a table of
reviews. These check (use case 12.6):

  1. bob declares a knowledge bank over the reviews' text and builds it: the
     platform embeds each review through the gateway on the project's key and
     writes the table knowledge_reviews in the project's namespace, as bob;
  2. a search by meaning finds the review about a split seam first, as bob and
     as alice (a reader), each under the project's rules; carol (no member) is
     refused;
  3. an assistant asks the MCP server's search_documents as alice, and gets
     the same passage, with its table and row to cite;
  4. a bank of the project's files: a Markdown page and an HTML one uploaded
     to its landing area are embedded (the HTML's tags taken out), and a
     search finds the page, with its key to cite;
  5. that bank has a schedule, so it's one of the project's pipelines, which
     builds it as the project's pipelines;
  6. a canvas step (an llm step's nearest mode) finds each review's nearest
     passage in the bank, itself, with where it's from;
  7. a review holding an email address stops the build at the gateway's
     guardrail, saying what it found.

With the stand-in, embeddings are words hashed into 256 dimensions, so a
search finds the reviews sharing its words; LiteLLM's model finds meaning.
The project is removed after.
"""
import json
import os

from semantic import Agent, agent_token

from lakekit import check, finish, sql
from projects import platform, projects, wait

PLATFORM = os.environ["PLATFORM_URL"]
NAME = "banked"
H = {"X-Platform-Request": "1"}
REVIEWS = [(1, "The jacket seam split after a week of use. Disappointing."), (2, "Delivery was late by three days and nobody told me."),
           (3, "Support answered in an hour and sorted the exchange."), (4, "Worth every cent, the tent survived a week of rain.")]
SPEC = {"apiVersion": "platform.storscale.io/v1alpha1", "kind": "Project", "metadata": {"name": NAME},
        "spec": {"description": "Knowledge banks: the knowledge suite's.",
                 "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "reader"}], "tables": {}, "files": {},
                 "llm": {"budget": 1},
                 "knowledge": [{"name": "reviews", "description": "What customers wrote", "tables": [{"table": "reviews", "column": "text", "id": "review_id"}]},
                               {"name": "manuals", "description": "The care and returns pages", "files": [{"prefix": "landing/uploads/"}],
                                "schedule": "0 3 * * *"}]}}
CARE = "# Caring for the jacket\n\nWash it cold. A split seam is mended free within a year of buying it."
RETURNS = "<html><head><style>p {color: red}</style></head><body><p>Returns are taken within 30 days, with the receipt.</p></body></html>"
NEAR = {"name": "near", "steps": [{"id": "reviews", "kind": "input", "table": "reviews", "x": 0, "y": 0},
                                  {"id": "near", "kind": "llm", "inputs": ["reviews"], "column": "text", "mode": "nearest", "bank": "reviews",
                                   "name": "passage", "x": 220, "y": 0},
                                  {"id": "out", "kind": "output", "inputs": ["near"], "table": "reviews_cited", "x": 440, "y": 0}]}


def main():
    bob, alice, carol = platform("bob"), platform("alice"), platform("carol")
    url = f"{PLATFORM}/api/projects/{NAME}"
    try:
        r = bob.put(url, data=json.dumps(SPEC), timeout=30)
        ready = wait(lambda: (lambda x: x if x and (x.get("status") or {}).get("phase") == "Ready" else None)((projects(bob) or {}).get(NAME)), 900, every=10)
        conns = lambda: {c["name"] for c in (bob.get(f"{url}/connections", timeout=30).json() or {}).get("connections", [])}
        keyed = wait(lambda: True if f"{NAME}-llm" in conns() else None, 300, every=5)
        sql("bob", "DROP TABLE IF EXISTS knowledge_reviews", "iceberg", NAME)   # a run before may have left it (the namespace outlives the project)
        sql("bob", "CREATE TABLE IF NOT EXISTS reviews (review_id integer, text varchar)", "iceberg", NAME)
        sql("bob", "DELETE FROM reviews", "iceberg", NAME)
        sql("bob", "INSERT INTO reviews VALUES " + ", ".join(f"({i}, '{t}')" for i, t in REVIEWS), "iceberg", NAME)
        check("bob makes a project with a knowledge bank over its reviews, and an LLM budget for its key", r.ok and ready is not None and keyed, r.text[:200])

        # 1. Built, as bob.
        listed = bob.get(f"{url}/knowledge", timeout=60).json()
        before = next((b for b in listed.get("banks", []) if b["name"] == "reviews"), {})
        built = bob.post(f"{url}/knowledge/reviews/build", headers=H, timeout=600)
        bj = built.json() if built.ok else {}
        after = next((b for b in (bob.get(f"{url}/knowledge", timeout=60).json() or {}).get("banks", []) if b["name"] == "reviews"), {})
        check("bob builds it: each review embedded on the project's key, written as knowledge_reviews in the project's namespace",
              before.get("chunks") is None and built.ok and bj.get("chunks") == 4 and bj.get("table") == f"iceberg.{NAME}.knowledge_reviews" and after.get("chunks") == 4,
              (before, built.status_code, built.text[:300], after))
        refused = alice.post(f"{url}/knowledge/reviews/build", headers=H, timeout=60)
        check("alice (a reader) can't build it", refused.status_code == 403, refused.status_code)

        # 2. Searched by meaning, as each person.
        def top(s):
            r = s.post(f"{url}/knowledge/reviews/search", headers=H, timeout=120, json={"query": "the seam split", "k": 2})
            return r, (r.json().get("hits") or [{}])[0] if r.ok else {}
        rb, hb = top(bob)
        ra, ha = top(alice)
        rc, _ = top(carol)
        check("a search finds the review about the split seam first, with its table and row, as bob and as alice",
              rb.ok and ra.ok and hb.get("ref") == "1" and ha.get("ref") == "1" and hb.get("source") == "reviews.text" and (hb.get("score") or 0) > 0.3,
              (rb.status_code, hb, ra.status_code, ha))
        check("carol (no member) is refused", rc.status_code in (403, 404), rc.status_code)

        # 3. An assistant, as alice.
        assistant = Agent(agent_token("alice"))
        out, err = assistant.call("search_documents", project=NAME, bank="reviews", query="the seam split", k=1)
        hit = ((out or {}).get("hits") or [{}])[0]
        check("an assistant asks search_documents as alice, and gets the same passage to cite", err is None and hit.get("ref") == "1" and "seam" in hit.get("text", ""), (err, out))

        # 4. A bank of the project's files: a page of Markdown and one of HTML, uploaded to its landing area.
        for fname, body, kind in (("care.md", CARE, "text/markdown"), ("returns.html", RETURNS, "text/html")):
            bob.post(f"{url}/uploads", headers=H, files={"file": (fname, body, kind)}, timeout=60)
        files = bob.post(f"{url}/knowledge/manuals/build", headers=H, timeout=600)
        fj = files.json() if files.ok else {}
        r = bob.post(f"{url}/knowledge/manuals/search", headers=H, timeout=120, json={"query": "a split seam", "k": 2})
        hit = ((r.json() if r.ok else {}).get("hits") or [{}])[0]
        check("a bank of files: the landing area's Markdown and HTML (its tags taken out) embedded, and a search finds the page and its key",
              files.ok and fj.get("files") == 2 and fj.get("chunks") == 2 and hit.get("ref", "").endswith("care.md")
              and hit.get("source") == "files:landing/uploads/" and "<" not in str(r.text), (files.status_code, files.text[:300], hit))

        # 5. Built on its schedule, as the project's pipelines.
        dag = f"knowledge_{NAME}_manuals"
        pipes = lambda: (bob.get(f"{url}/pipelines", timeout=60).json() or {}).get("pipelines", [])
        loaded = wait(lambda: True if any(p["id"] == dag for p in pipes()) else None, 600, every=10)
        started = bob.post(f"{url}/pipelines/{dag}/runs", headers=H, timeout=60) if loaded else None
        run_id = (started.json() if started is not None and started.ok else {}).get("run")
        state = lambda: next((x["state"] for p in pipes() if p["id"] == dag for x in p.get("runs", []) if x["id"] == run_id and x["state"] in ("success", "failed")), None)
        final = wait(state, 600, every=10) if run_id else None
        check("a bank with a schedule is one of the project's pipelines, which builds it as the project's pipelines", final == "success", (bool(loaded), run_id, final))

        # 6. The canvas: each review's nearest passage in the bank, and where it's from.
        near = bob.post(f"{url}/flows/preview", headers=H, json={"flow": NEAR, "step": "near"}, timeout=300)
        nj = near.json() if near.ok else {}
        cols = nj.get("columns") or []
        rows = [dict(zip(cols, row)) for row in nj.get("rows") or []]
        check("a canvas step finds each review's nearest passage in the bank (itself), with its source to cite",
              near.ok and cols[-2:] == ["passage", "passage_source"] and len(rows) == 4
              and all(r["passage_source"] == f"reviews.text:{r['review_id']}" and r["passage"] == r["text"] for r in rows), (near.status_code, near.text[:400]))

        # 7. The guardrail, on what's embedded.
        sql("bob", "INSERT INTO reviews VALUES (5, 'Write back at jane.doe@example.com about my order.')", "iceberg", NAME)
        stopped = bob.post(f"{url}/knowledge/reviews/build", headers=H, timeout=600)
        check("a review holding an email address stops the build at the gateway's guardrail, saying what it found",
              stopped.status_code == 422 and "an email address" in stopped.text and "jane" not in stopped.text, (stopped.status_code, stopped.text[:300]))
    finally:
        bob.delete(url, headers=H, timeout=30)
        wait(lambda: NAME not in (projects(bob) or {}), 300)
    finish()


if __name__ == "__main__":
    main()
