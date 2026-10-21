"""An agent with a prompt and no notebook (use case 12.7): the platform's
runner does what the prompt says with the agent's tools. It signs in as the
agent (its Keycloak client), lists the tools the MCP server gives it, and
asks the model through the gateway on the agent's key, giving it those tools;
each tool the model asks for is called as the agent, and its answer given
back, until the model answers with its report (at most MAX_STEPS rounds).
The transcript is kept as a notebook, the run's record, as a notebook agent's
executed notebook is.
"""
import json

import requests

MAX_STEPS = 8
RESULT_CHARS = 20000   # of a tool's answer, given to the model


class Refused(RuntimeError):
    """The gateway or the MCP server said no (the guardrail, the budget, a tool not given)."""


def token(token_url, client_id, secret):
    r = requests.post(token_url, timeout=30, data={"grant_type": "client_credentials", "client_id": client_id, "client_secret": secret})
    r.raise_for_status()
    return r.json()["access_token"]


class MCP:
    def __init__(self, url, get_token):
        self.url, self.get_token, self.n = url, get_token, 0
        self.rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "storscale-agent-runner", "version": "1"}})

    def rpc(self, method, params=None):
        self.n += 1
        r = requests.post(self.url, timeout=120, json={"jsonrpc": "2.0", "id": self.n, "method": method, **({"params": params} if params else {})},
                          headers={"Accept": "application/json, text/event-stream", "Authorization": f"Bearer {self.get_token()}"})
        r.raise_for_status()
        return r.json()

    def tools(self):
        return self.rpc("tools/list").get("result", {}).get("tools", [])

    def call(self, name, args):
        res = self.rpc("tools/call", {"name": name, "arguments": args}).get("result", {})
        text = " ".join(c.get("text", "") for c in res.get("content", []))
        return (res.get("structuredContent") if res.get("structuredContent") is not None else text), bool(res.get("isError"))


def ask(llm_url, key, user, messages, tools):
    r = requests.post(f"{llm_url.rstrip('/')}/chat/completions", timeout=300, headers={"Authorization": f"Bearer {key}"},
                      json={"model": "default", "messages": messages, "tools": tools, "user": user})
    if r.status_code in (400, 422) and ("guardrail" in r.text or "udget" in r.text):
        try:
            why = r.json()["error"]
            why = why.get("message") if isinstance(why, dict) else why
        except (ValueError, KeyError):
            why = r.text[:300]
        raise Refused(f"the gateway refused: {why}")
    r.raise_for_status()
    return r.json()["choices"][0]["message"]


def cell(text):
    return {"cell_type": "markdown", "metadata": {}, "source": text}


def run(project, agent, prompt, description, mcp_url, llm_url, llm_key, token_url, client_id, secret, record):
    """Does the prompt; writes the transcript to record (an .ipynb); is the report."""
    user = f"service-account-{client_id}"
    mcp = MCP(mcp_url, lambda: token(token_url, client_id, secret))
    listed = mcp.tools()
    tools = [{"type": "function", "function": {"name": t["name"], "description": t.get("description") or "",
                                               "parameters": t.get("inputSchema") or {"type": "object", "properties": {}}}} for t in listed]
    system = (f"You are the agent {agent} of the project {project}" + (f": {description}" if description else "") + ". "
              "Use the tools to do the task; each answers as you, under the project's rules. When you're done, answer with your report alone.")
    messages = [{"role": "system", "content": system}, {"role": "user", "content": prompt}]
    cells = [cell(f"# The agent {agent} of {project}\n\n**The task**\n\n{prompt}\n\n**Its tools:** {', '.join(t['name'] for t in listed) or 'none'}")]
    report = None
    try:
        for step in range(1, MAX_STEPS + 1):
            msg = ask(llm_url, llm_key, user, messages, tools)
            calls = msg.get("tool_calls") or []
            if not calls:
                report = (msg.get("content") or "").strip()
                cells.append(cell(f"## The report\n\n{report}"))
                break
            messages.append({"role": "assistant", "content": msg.get("content"), "tool_calls": calls})
            for c in calls:
                fn = c.get("function") or {}
                try:
                    args = json.loads(fn.get("arguments") or "{}")
                except ValueError:
                    args = {}
                result, refused = mcp.call(fn.get("name"), args)
                text = result if isinstance(result, str) else json.dumps(result)
                cells.append(cell(f"**Step {step}: {fn.get('name')}** `{json.dumps(args)}`" + (" (refused)" if refused else "") +
                                  f"\n\n```\n{text[:3000]}\n```"))
                messages.append({"role": "tool", "tool_call_id": c.get("id"), "content": text[:RESULT_CHARS]})
        else:
            raise RuntimeError(f"no report after {MAX_STEPS} rounds of tools")
    except Exception as e:
        cells.append(cell(f"## It stopped\n\n{e}"))
        raise
    finally:
        with open(record, "w") as f:
            json.dump({"nbformat": 4, "nbformat_minor": 5, "metadata": {"title": f"{agent} of {project}"}, "cells": cells}, f)
    return report
