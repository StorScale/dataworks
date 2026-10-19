"""Where personal data is, found and declared (use case 12.5).

Each round (CLASSIFY_EVERY seconds), for every project's tables as the
catalog knows them:

  1. the columns a project declares (spec.tables.people: the column that
     identifies a person) are tagged Protection.personal in the catalog,
     confirmed;
  2. the other text columns are looked at: their names, and a sample of
     their values read from Trino as the catalog's own account (which reads
     every table, to describe them); one that looks like email addresses,
     phone numbers, card numbers, national IDs or bank accounts is tagged
     Protection.personal, suggested; salaries and balances Protection.financial;
     diagnoses Protection.health. A suggestion someone removed in the catalog
     isn't made again;
  3. what's tagged (declared, suggested, confirmed) is written to the
     platform's bucket, classification/<project>.json, where the operator
     turns it into Ranger's masks and the project's pages show it.

The catalog's own classification is Protection; its tags are the install's
rules' names (personal, financial, health). Detection here is by plain
patterns, deterministic and quick; OpenMetadata's own PII processor (presidio
and spaCy, some 600 MB in the image) can take its place when names and
addresses in free text need finding.
"""
import json
import logging
import re
import time

import requests

log = logging.getLogger("catalog-sync")

PROTECTION = "Protection"
TAGS = {
    "personal": "Identifies a person, or is about one: names, contact details, identifiers, dates of birth.",
    "financial": "A person's money: salaries, balances, card and account numbers' amounts.",
    "health": "A person's health: diagnoses, prescriptions, conditions.",
}
TEXT_TYPES = {"VARCHAR", "CHAR", "STRING", "TEXT", "JSON"}
SAMPLE_ROWS = 200
MIN_SHARE = 0.6   # of the sampled non-empty values a pattern must match

# By name: a column called like this is tagged without looking at its values.
BY_NAME = [
    (re.compile(r"(^|_)(e_?mail|phone|mobile|telephone|ssn|social_security|passport|national_id|tax_id|iban|"
                r"card_number|cc_number|credit_card|date_of_birth|dob|birth_?date|first_name|last_name|surname|full_name|"
                r"home_address|street|postal_code|zip_code)($|_)"), "personal"),
    (re.compile(r"(^|_)(salary|wage|income|net_worth|credit_score|account_balance|balance)($|_)"), "financial"),
    (re.compile(r"(^|_)(diagnos\w*|icd_?10|medical|prescription|health_condition|condition)($|_)"), "health"),
]

EMAIL = re.compile(r"^[^@\s]+@[^@\s]+\.[a-z]{2,}$", re.I)
PHONE = re.compile(r"^\+?[0-9][0-9 ().-]{7,18}[0-9]$")
SSN = re.compile(r"^[0-9]{3}-[0-9]{2}-[0-9]{4}$")
IBAN = re.compile(r"^[A-Z]{2}[0-9]{2}[A-Z0-9]{11,30}$")
CARD = re.compile(r"^[0-9][0-9 -]{11,21}[0-9]$")


def luhn(s):
    digits = [int(c) for c in s if c.isdigit()]
    if not 13 <= len(digits) <= 19:
        return False
    total = 0
    for i, d in enumerate(reversed(digits)):
        if i % 2:
            d *= 2
            if d > 9:
                d -= 9
        total += d
    return total % 10 == 0


# By value: what a column's sampled values look like.
BY_VALUE = [
    ("email", lambda v: bool(EMAIL.match(v)), "personal"),
    ("card number", lambda v: bool(CARD.match(v)) and luhn(v), "personal"),
    ("national ID", lambda v: bool(SSN.match(v)), "personal"),
    ("bank account", lambda v: bool(IBAN.match(v.replace(" ", ""))), "personal"),
    ("phone number", lambda v: bool(PHONE.match(v)) and sum(c.isdigit() for c in v) >= 8, "personal"),
]


def ensure_classification(om, h):
    """The Protection classification and its tags, made once."""
    r = requests.get(f"{om}/v1/classifications/name/{PROTECTION}", headers=h, timeout=10)
    if r.status_code == 404:
        requests.post(f"{om}/v1/classifications", headers=h, timeout=10, json={
            "name": PROTECTION, "displayName": "Protection",
            "description": "What the platform protects by tag: a column tagged here is masked for everyone but the groups the install excepts (use case 12.5).",
            "mutuallyExclusive": False}).raise_for_status()
    for name, desc in TAGS.items():
        r = requests.get(f"{om}/v1/tags/name/{PROTECTION}.{name}", headers=h, timeout=10)
        if r.status_code == 404:
            requests.post(f"{om}/v1/tags", headers=h, timeout=10, json={"name": name, "classification": PROTECTION, "description": desc}).raise_for_status()


def column_tags(col):
    """A column's Protection tags: {tag name: (state, labelType)}."""
    out = {}
    for t in col.get("tags") or []:
        fqn = t.get("tagFQN", "")
        if fqn.startswith(PROTECTION + "."):
            out[fqn.split(".", 1)[1]] = (t.get("state", "Confirmed"), t.get("labelType", "Manual"))
    return out


def tag_column(om, h, table, index, tag, confirmed):
    """One tag on one column, as a JSON patch on the table."""
    col = table["columns"][index]
    label = {"tagFQN": f"{PROTECTION}.{tag}", "source": "Classification",
             "labelType": "Manual" if confirmed else "Automated", "state": "Confirmed" if confirmed else "Suggested"}
    n = len(col.get("tags") or [])
    patch = [{"op": "add", "path": f"/columns/{index}/tags/{n}", "value": label}]
    if n == 0 and col.get("tags") is None:
        patch = [{"op": "add", "path": f"/columns/{index}/tags", "value": [label]}]
    r = requests.patch(f"{om}/v1/tables/{table['id']}", headers={**h, "Content-Type": "application/json-patch+json"},
                       timeout=10, data=json.dumps(patch))
    if not r.ok:
        log.warning("classification: tagging %s.%s with %s: %s", table["name"], col["name"], tag, r.text[:200])
        return False
    col.setdefault("tags", []).append(label)
    return True


def looks_like(values):
    """What a column's values look like, by the first pattern most of them match: (kind, tag) or None."""
    vals = [v.strip() for v in values if isinstance(v, str) and v.strip()]
    if len(vals) < 3:
        return None
    for kind, match, tag in BY_VALUE:
        if sum(1 for v in vals if match(v)) >= MIN_SHARE * len(vals):
            return kind, tag
    return None


def sample(trino, catalog, schema, table, columns):
    """A sample of a table's text columns' values, as the catalog's account."""
    if not columns:
        return {}
    cols = ", ".join(f'"{c}"' for c in columns)
    cur = trino(catalog, schema).cursor()
    try:
        cur.execute(f'SELECT {cols} FROM "{table}" LIMIT {SAMPLE_ROWS}')
        rows = cur.fetchall()
    finally:
        cur.close()
    return {c: [r[i] for r in rows] for i, c in enumerate(columns)}


def classify_project(om, h, service, p, trino, before):
    """One project's tables: tags made, and what's tagged now (the file's columns)."""
    spec = p.get("spec", {})
    t = spec.get("tables")
    if t is None:
        return [], []
    catalog, schema = t.get("catalog") or "iceberg", t.get("namespace") or p["metadata"]["name"].replace("-", "_")
    declared = {(pk["table"], pk["column"]) for pk in t.get("people") or []}
    dismissed = {(d["table"], d["column"], d["tag"]) for d in before.get("dismissed", [])}
    suggested_before = {(d["table"], d["column"], d["tag"]) for d in before.get("suggested", [])}
    r = requests.get(f"{om}/v1/tables", headers=h, timeout=30,
                     params={"databaseSchema": f"{service}.{catalog}.{schema}", "fields": "columns,tags", "limit": 200})
    if not r.ok:
        log.warning("classification: %s's tables: %s", p["metadata"]["name"], r.text[:200])
        return [], []
    now, suggested = [], []
    for table in r.json().get("data", []):
        if table.get("deleted"):
            continue
        name = table["name"]
        cols = table.get("columns") or []
        # Declared: confirmed, by the project.
        for i, col in enumerate(cols):
            if (name, col["name"]) in declared and "personal" not in column_tags(col):
                tag_column(om, h, table, i, "personal", confirmed=True)
        # Found: by name, then by a sample of the values.
        text = [c["name"] for c in cols if (c.get("dataType") or "").upper() in TEXT_TYPES and not column_tags(c)]
        values = {}
        try:
            values = sample(trino, catalog, schema, name, text) if text else {}
        except Exception as e:  # noqa: BLE001 (a table that can't be read is told, not fatal)
            log.warning("classification: sampling %s.%s: %s", schema, name, str(e)[:160])
        for i, col in enumerate(cols):
            if column_tags(col) or (name, col["name"]) in declared:
                continue
            found = None
            for pattern, tag in BY_NAME:
                if pattern.search(col["name"].lower()):
                    found = ("its name", tag)
                    break
            if not found and col["name"] in values:
                found = looks_like(values[col["name"]])
            if not found:
                continue
            kind, tag = found
            if (name, col["name"], tag) in dismissed or ((name, col["name"], tag) in suggested_before and tag not in column_tags(col)):
                continue   # someone took it off: not again
            if tag_column(om, h, table, i, tag, confirmed=False):
                log.info("classification: %s.%s.%s looks personal (%s): tagged %s, suggested", schema, name, col["name"], kind, tag)
                suggested.append({"table": name, "column": col["name"], "tag": tag, "by": kind})
        for col in cols:
            for tag, (state, label) in column_tags(col).items():
                now.append({"table": name, "column": col["name"], "tag": tag, "state": state.lower(),
                            "type": (col.get("dataType") or "").lower(),   # the mask must give the column's type back
                            "declared": (name, col["name"]) in declared, "automated": label == "Automated"})
    return now, suggested


def run(om, h, service, projects, trino, s3, bucket):
    """A round: every project's tables classified, and the files written."""
    ensure_classification(om, h)
    for p in projects:
        name = p["metadata"]["name"]
        key = f"classification/{name}.json"
        before = {}
        try:
            before = json.loads(s3.get_object(Bucket=bucket, Key=key)["Body"].read())
        except Exception:  # noqa: BLE001 (none yet)
            pass
        now, suggested = classify_project(om, h, service, p, trino, before)
        if not now and not suggested and not before:
            continue
        tagged = {(c["table"], c["column"], c["tag"]) for c in now}
        remembered = {(d["table"], d["column"], d["tag"]): d for d in before.get("suggested", [])}
        for s in suggested:
            remembered[(s["table"], s["column"], s["tag"])] = s
        dismissed = [d for d in remembered.values() if (d["table"], d["column"], d["tag"]) not in tagged]
        doc = {"project": name, "at": int(time.time()), "columns": now,
               "suggested": list(remembered.values()), "dismissed": dismissed}
        s3.put_object(Bucket=bucket, Key=key, Body=json.dumps(doc).encode(), ContentType="application/json")
    # A project gone takes its memory with it: a new project of the same name
    # starts with nothing dismissed.
    names = {p["metadata"]["name"] for p in projects}
    try:
        for o in s3.list_objects_v2(Bucket=bucket, Prefix="classification/").get("Contents") or []:
            k = o["Key"]
            if k.endswith(".json") and k[len("classification/"):-len(".json")] not in names:
                s3.delete_object(Bucket=bucket, Key=k)
    except Exception as e:  # noqa: BLE001 (next round)
        print("classify: leftover files:", e, flush=True)
