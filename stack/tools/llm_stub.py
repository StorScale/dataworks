"""A stand-in for the LLM gateway (use case 12.1), for CI and installs without
a provider: the little of LiteLLM's API the platform uses, with a budget per
key that it enforces.

  POST /key/generate   {key_alias, max_budget, metadata}  -> {key}      (the master key)
  POST /key/delete     {keys: [...]}                                     (the master key)
  GET  /key/info?key=  -> {info: {key_alias, max_budget, spend, metadata}} (the master key, or the key itself)
  GET  /key/list       -> {keys: [{key, key_alias, max_budget, spend}]}  (the master key)
  POST /chat/completions, /v1/chat/completions  (a key): an answer that repeats the question; each call
                       costs 0.01 per 100 characters, and a key past its budget gets HTTP 400 as LiteLLM gives it
  GET  /health/liveliness, /healthz

Keys and spend are kept in LLM_STUB_STATE (a JSON file; default in memory).
The master key is LLM_MASTER_KEY.
"""
import json
import os
import secrets
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

MASTER = os.environ.get("LLM_MASTER_KEY", "sk-master")
STATE = os.environ.get("LLM_STUB_STATE", "")
PORT = int(os.environ.get("LLM_STUB_PORT", "4000"))
COST_PER_100_CHARS = 0.01

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


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        print("llm-stub:", fmt % args, flush=True)

    def answer(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
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
                return self.answer(200, {"keys": [{"key": k, **v} for k, v in keys.items()]})
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
        if u.path in ("/chat/completions", "/v1/chat/completions"):
            with lock:
                info = keys.get(tok)
                if not info:
                    return self.answer(401, {"error": {"message": "Authentication Error: invalid key"}})
                b = self.body()
                text = " ".join(str(m.get("content", "")) for m in b.get("messages", []))
                cost = COST_PER_100_CHARS * max(1, len(text) / 100)
                if info.get("max_budget") is not None and info["spend"] + cost > float(info["max_budget"]):
                    return self.answer(400, {"error": {"message": f"Budget has been exceeded! Current cost: {info['spend']:.2f}, Max budget: {info['max_budget']}",
                                                       "type": "budget_exceeded", "code": "400"}})
                info["spend"] = round(info["spend"] + cost, 6)
                save()
            reply = "You asked: " + text[:200]
            return self.answer(200, {"id": "chatcmpl-" + secrets.token_hex(6), "object": "chat.completion", "model": b.get("model", "stub"),
                                     "choices": [{"index": 0, "message": {"role": "assistant", "content": reply}, "finish_reason": "stop"}],
                                     "usage": {"prompt_tokens": len(text) // 4, "completion_tokens": len(reply) // 4, "total_tokens": (len(text) + len(reply)) // 4}})
        self.answer(404, {"error": {"message": "no such route"}})


if __name__ == "__main__":
    load()
    print(f"llm-stub: the gateway's stand-in on :{PORT}", flush=True)
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
