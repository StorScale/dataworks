"""A stand-in for the LLM gateway (use case 12.1), for CI and installs without
a provider: the little of LiteLLM's API the platform uses, with a budget per
key that it enforces.

  POST /key/generate   {key_alias, max_budget, metadata}  -> {key}      (the master key)
  POST /key/delete     {keys: [...]}                                     (the master key)
  GET  /key/info?key=  -> {info: {key_alias, max_budget, spend, metadata}} (the master key, or the key itself)
  GET  /key/list       -> {keys: [{key, token, key_alias, max_budget, spend}], total_pages: 1}  (the master key; LiteLLM's
                       return_full_object, size and page are taken and ignored: one page, the whole objects, the key as its token too)
  POST /chat/completions, /v1/chat/completions  (a key): an answer that repeats the question; each call
                       costs 0.01 per 100 characters, and a key past its budget gets HTTP 422 as LiteLLM gives it;
                       a question carrying what may not leave (llm_guard: a masked value's shape, a secret)
                       is refused first, HTTP 400, as the guardrail refuses it at LiteLLM (LLM_GUARDRAILS=false turns it off);
                       a question shaped as an llm step's ("Answer with a JSON array of N strings") gets N answers (the
                       first label, or "ok"), so a flow's llm step runs in CI; x-litellm-response-cost says the cost, as LiteLLM's does
  POST /embeddings, /v1/embeddings  (a key): a vector per input, words hashed into 64 dimensions (shared words are near),
                       under the guardrail and the budget, as LiteLLM's would be
  GET  /health/liveliness, /healthz

Keys and spend are kept in LLM_STUB_STATE (a JSON file; default in memory).
The master key is LLM_MASTER_KEY.
"""
import json
import os
import re
import secrets
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import llm_guard

MASTER = os.environ.get("LLM_MASTER_KEY", "sk-master")
STATE = os.environ.get("LLM_STUB_STATE", "")
PORT = int(os.environ.get("LLM_STUB_PORT", "4000"))
COST_PER_100_CHARS = 0.01
GUARDRAILS = os.environ.get("LLM_GUARDRAILS", "true").lower() not in ("false", "0", "no", "off")

lock = threading.Lock()
keys = {}   # key -> {key_alias, max_budget, spend, metadata, created}


def load():
    global keys
    if STATE and os.path.exists(STATE):
        with open(STATE) as f:
            keys = json.load(f)


def save():
    if STATE:
        with open(STATE + ".tmp", "w") as f:
            json.dump(keys, f)
        os.replace(STATE + ".tmp", STATE)


def embed(text, dims=64):
    """A text as the stand-in embeds it: its words (and their halves, so seam ~ seams) hashed into dims, normalized."""
    import hashlib
    import math
    v = [0.0] * dims
    for w in re.findall(r"[a-z0-9]+", text.lower()):
        for part in (w, w[: max(3, len(w) - 1)]):
            h = int(hashlib.sha256(part.encode()).hexdigest(), 16)
            v[h % dims] += 1.0 if (h >> 8) % 2 else -1.0
    n = math.sqrt(sum(x * x for x in v)) or 1.0
    return [x / n for x in v]


def resolve(text):
    """A semantic test's question, as the stand-in reads it: the metrics named in the question (by their
    names, with spaces for underscores, or a synonym the prompt lists), and the dimensions whose last part
    it names (region for order__region, quarter for metric_time__quarter)."""
    q = (re.search(r"^Question: (.*)$", text, re.M) or [None, ""])[1].lower()
    metrics, dims, section = [], [], None
    for line in text.splitlines():
        if line in ("Metrics:", "Dimensions:"):
            section = line
            continue
        m = re.match(r"- ([a-z0-9_]+)(?::.*?)?(?: \(also called: (.*)\))?$", line)
        if not m:
            continue
        name = m.group(1)
        if section == "Metrics:":
            words = [name.replace("_", " ")] + [w.strip().lower() for w in (m.group(2) or "").split(",") if w.strip()]
            if any(re.search(r"\b" + re.escape(w) + r"\b", q) for w in words):
                metrics.append(name)
        elif section == "Dimensions:" and name != "metric_time":
            last = name.split("__")[-1].replace("_", " ")
            if re.search(r"\b" + re.escape(last) + r"s?\b", q):
                dims.append(name)
    if any(d.startswith("metric_time__") for d in dims):   # the time grain asked for, not each one the word matches
        dims = [d for d in dims if not d.startswith("metric_time__")] + [next(d for d in dims if d.startswith("metric_time__"))]
    return {"metrics": metrics, "group_by": dims}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        print("llm-stub:", fmt % args, flush=True)

    def answer(self, code, body, cost=None):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        if cost is not None:
            self.send_header("x-litellm-response-cost", f"{cost:.6f}")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def bearer(self):
        h = self.headers.get("Authorization", "")
        return h[7:] if h.startswith("Bearer ") else ""

    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(n) or b"{}") if n else {}

    def do_GET(self):
        u = urlparse(self.path)
        q = parse_qs(u.query)
        if u.path in ("/health/liveliness", "/healthz", "/health"):
            return self.answer(200, {"status": "alive"})
        tok = self.bearer()
        if u.path == "/key/info":
            key = (q.get("key") or [tok])[0]
            with lock:
                info = keys.get(key)
            if tok != MASTER and tok != key:
                return self.answer(401, {"error": {"message": "Authentication Error: invalid key"}})
            if not info:
                return self.answer(404, {"error": {"message": "key not found"}})
            return self.answer(200, {"key": key[:8] + "...", "info": info})
        if u.path == "/key/list":
            if tok != MASTER:
                return self.answer(401, {"error": {"message": "Authentication Error: not the master key"}})
            with lock:
                return self.answer(200, {"keys": [{"key": k, "token": k, **v} for k, v in keys.items()], "total_count": len(keys), "total_pages": 1})
        self.answer(404, {"error": {"message": "no such route"}})

    def do_POST(self):
        u = urlparse(self.path)
        tok = self.bearer()
        if u.path == "/key/generate":
            if tok != MASTER:
                return self.answer(401, {"error": {"message": "Authentication Error: not the master key"}})
            b = self.body()
            key = "sk-" + secrets.token_hex(16)
            with lock:
                keys[key] = {"key_alias": b.get("key_alias", ""), "max_budget": b.get("max_budget"), "spend": 0.0,
                             "budget_duration": b.get("budget_duration"), "metadata": b.get("metadata") or {}, "created": time.time()}
                save()
            return self.answer(200, {"key": key, "key_alias": b.get("key_alias", ""), "max_budget": b.get("max_budget")})
        if u.path == "/key/delete":
            if tok != MASTER:
                return self.answer(401, {"error": {"message": "Authentication Error: not the master key"}})
            b = self.body()
            with lock:
                gone = [k for k in b.get("keys") or [] if keys.pop(k, None) is not None]
                save()
            return self.answer(200, {"deleted_keys": gone})
        if u.path in ("/embeddings", "/v1/embeddings"):   # a stand-in's embeddings: words hashed into 64 dimensions, so shared words are near
            with lock:
                info = keys.get(tok)
                if not info:
                    return self.answer(401, {"error": {"message": "Authentication Error: invalid key"}})
                b = self.body()
                texts = b.get("input") if isinstance(b.get("input"), list) else [b.get("input") or ""]
                kinds = llm_guard.found([{"content": t} for t in texts]) if GUARDRAILS else []
                if kinds:
                    return self.answer(400, {"error": {"message": llm_guard.refusal(kinds), "type": "guardrail", "code": "400"}})
                cost = 0.0001 * max(1, sum(len(t) for t in texts) / 1000)
                if info.get("max_budget") is not None and info["spend"] + cost > float(info["max_budget"]):
                    return self.answer(422, {"error": {"message": "Budget has been exceeded!", "type": "budget_exceeded", "code": "422"}})
                info["spend"] = round(info["spend"] + cost, 6)
                save()
            return self.answer(200, {"object": "list", "model": b.get("model", "stub"),
                                     "data": [{"object": "embedding", "index": i, "embedding": embed(t)} for i, t in enumerate(texts)]}, cost=cost)
        if u.path in ("/chat/completions", "/v1/chat/completions"):
            with lock:
                info = keys.get(tok)
                if not info:
                    return self.answer(401, {"error": {"message": "Authentication Error: invalid key"}})
                b = self.body()
                kinds = llm_guard.found(b.get("messages")) if GUARDRAILS else []
                if kinds:
                    print(f"llm-stub: refused for {info.get('key_alias')}: {', '.join(kinds)}", flush=True)
                    return self.answer(400, {"error": {"message": llm_guard.refusal(kinds), "type": "guardrail", "code": "400"}})
                text = " ".join(str(m.get("content", "")) for m in b.get("messages", []))
                cost = COST_PER_100_CHARS * max(1, len(text) / 100)
                if info.get("max_budget") is not None and info["spend"] + cost > float(info["max_budget"]):
                    return self.answer(422, {"error": {"message": f"Budget has been exceeded! Current cost: {info['spend']:.2f}, Max budget: {info['max_budget']}",
                                                       "type": "budget_exceeded", "code": "422"}})
                info["spend"] = round(info["spend"] + cost, 6)
                save()
            reply = "You asked: " + text[:200]
            if "Translate the question into a metric query" in text:   # a semantic test's question: words matched to names
                reply = json.dumps(resolve(text))
            shaped = re.search(r"Answer with a JSON array of (\d+) strings", text)
            if shaped:   # an llm step's question: an answer per item, the first label when there are labels
                labels = re.search(r"exactly one of: (.+?)\.\n", text)
                one = labels.group(1).split(",")[0].strip() if labels else "ok"
                reply = json.dumps([one] * int(shaped.group(1)))
            return self.answer(200, {"id": "chatcmpl-" + secrets.token_hex(6), "object": "chat.completion", "model": b.get("model", "stub"),
                                     "choices": [{"index": 0, "message": {"role": "assistant", "content": reply}, "finish_reason": "stop"}],
                                     "usage": {"prompt_tokens": len(text) // 4, "completion_tokens": len(reply) // 4, "total_tokens": (len(text) + len(reply)) // 4}}, cost=cost)
        self.answer(404, {"error": {"message": "no such route"}})


if __name__ == "__main__":
    load()
    print(f"llm-stub: the gateway's stand-in on :{PORT}", flush=True)
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
