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
  4. a review holding an email address stops the build at the gateway's
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
                 "members": [{"group": "engineers", "role": "editor"}, {"user": "alice", "role": "reader"}], "tables": {},
                 "llm": {"budget": 1},
                 "knowledge": [{"name": "reviews", "description": "What customers wrote", "tables": [{"table": "reviews", "column": "text", "id": "review_id"}]}]}}


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

        # 4. The guardrail, on what's embedded.
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
