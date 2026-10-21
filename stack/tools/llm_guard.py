"""The gateway's guardrail (use case 12.1): what may not leave the platform
for a model, and the refusal when it would.

A question is refused, before it reaches the model, when its text carries
  - a value the platform masks (use case 12.5): what looks like a card
    number (Luhn-checked), a national ID, a bank account number, an email
    address, or a phone number; the same patterns catalog-sync tags a column
    personal by, so what a reader sees hashed or hidden in a table doesn't
    reach a model in a prompt either;
  - a secret of the platform's: one of its keys by shape (the gateway's own
    keys, GitLab's and GitHub's tokens, AWS keys, bearer tokens and JWTs,
    private keys), or one of its secrets' names with a value beside it
    (LLM_MASTER_KEY=..., a *_PASSWORD, *_SECRET, *_TOKEN or *_API_KEY);
  - a term the install keeps in (LLM_FORBIDDEN_TERMS: a codename, a client's
    name), as a word or a pattern.

The refusal names what was found, never the value. The same module serves
LiteLLM (StorScaleGuardrail, a pre-call guardrail in the chart's config) and
the stand-in (llm_stub.py), so the two refuse the same things.
"""
import os
import re

# What a mask would hide: whole tokens of the text, as catalog-sync looks at a column's values.
EMAIL = re.compile(r"[^@\s<>()\[\],;:\"']+@[^@\s<>()\[\],;:\"']+\.[a-z]{2,}", re.I)
SSN = re.compile(r"(?<![0-9-])[0-9]{3}-[0-9]{2}-[0-9]{4}(?![0-9-])")
IBAN = re.compile(r"(?<![A-Z0-9])[A-Z]{2}[0-9]{2}(?:[ ]?[A-Z0-9]{4}){2,7}(?:[ ]?[A-Z0-9]{1,4})?(?![A-Z0-9])")
CARD = re.compile(r"(?<![0-9])[0-9](?:[ -]?[0-9]){11,18}(?![0-9])")
# A phone number in text is written in groups (+1 (415) 555-0123, 0171 234 5678), ten to fifteen digits: a bare run of
# digits is an ID or an amount, and a date has too few.
PHONE = re.compile(r"(?<![0-9A-Za-z+])(?:\+[0-9]{1,3}[ .-]?)?(?:\(?[0-9]{2,4}\)?[ .-]){1,3}[0-9]{3,4}(?![0-9A-Za-z])")

# The platform's secrets, by shape, and by name with a value beside it.
SECRETS = [
    ("a gateway key", re.compile(r"\bsk-[0-9a-f]{32}\b")),
    ("a GitLab token", re.compile(r"\bglpat-[A-Za-z0-9_-]{20,}")),
    ("a GitHub token", re.compile(r"\bgh[pousr]_[A-Za-z0-9]{36,}")),
    ("an AWS key", re.compile(r"\b(?:AKIA|ASIA)[0-9A-Z]{16}\b")),
    ("a signed token", re.compile(r"\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}")),
    ("a bearer token", re.compile(r"\bBearer\s+[A-Za-z0-9._~+/=-]{16,}", re.I)),
    ("a private key", re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----")),
    ("a secret by its name", re.compile(r"\b(?:LLM_MASTER_KEY|STORSCALE_LLM_KEY|STORSCALE_AGENT_SECRET|[A-Z][A-Z0-9_]*(?:PASSWORD|SECRET|TOKEN|API_KEY))\s*[=:]\s*[^\s,;\"']{8,}")),
]


def forbidden_terms():
    """The install's own words and patterns that may not leave (a codename, a client's name):
    LLM_FORBIDDEN_TERMS, one a line; a line in slashes is a pattern (/acme-\\d+/), any other
    a word or phrase, matched whole and in any case."""
    out = []
    for line in os.environ.get("LLM_FORBIDDEN_TERMS", "").splitlines():
        line = line.strip()
        if not line:
            continue
        if len(line) > 2 and line.startswith("/") and line.endswith("/"):
            try:
                out.append(re.compile(line[1:-1], re.I))
            except re.error:
                pass   # a pattern that isn't one is skipped, not the whole list
        else:
            out.append(re.compile(r"(?<!\w)" + re.escape(line) + r"(?!\w)", re.I))
    return out


TERMS = forbidden_terms()


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


def text_of(messages):
    """Every message's text, whatever shape its content takes (a string, or OpenAI's list of parts)."""
    out = []
    for m in messages or []:
        c = m.get("content") if isinstance(m, dict) else None
        if isinstance(c, str):
            out.append(c)
        elif isinstance(c, list):
            out.extend(str(p.get("text", "")) for p in c if isinstance(p, dict))
    return "\n".join(out)


def found(messages):
    """What in the messages may not leave, as names: [] when nothing."""
    text = text_of(messages)
    kinds = []
    text, cards = without(text, CARD, luhn)
    if cards:
        kinds.append("a card number")
    if SSN.search(text):
        kinds.append("a national ID")
    text, accounts = without(text, IBAN, iban_checks)
    if accounts:
        kinds.append("a bank account number")
    if EMAIL.search(text):
        kinds.append("an email address")
    if any(10 <= sum(c.isdigit() for c in m.group()) <= 15 for m in PHONE.finditer(text)):
        kinds.append("a phone number")
    for name, pattern in SECRETS:
        if pattern.search(text):
            kinds.append(name)
    if any(t.search(text) for t in TERMS):
        kinds.append("a term the company keeps in")
    return kinds


def without(text, pattern, check):
    """The text with the pattern's matches that pass the check blanked (so a card's digits aren't a phone number too), and how many."""
    n = 0

    def blank(m):
        nonlocal n
        if check(m.group()):
            n += 1
            return " " * len(m.group())
        return m.group()
    return pattern.sub(blank, text), n


def iban_checks(iban):
    """An IBAN's check: its country and check digits moved to the end, letters as numbers, mod 97 is 1."""
    s = iban.replace(" ", "").upper()
    if not 15 <= len(s) <= 34:
        return False
    moved = s[4:] + s[:4]
    try:
        return int("".join(str(int(c, 36)) for c in moved)) % 97 == 1
    except ValueError:
        return False


def refusal(kinds):
    """The gateway's answer, naming what was found and never the value."""
    what = ", ".join(kinds[:-1]) + (" and " if len(kinds) > 1 else "") + kinds[-1]
    return f"the question holds {what}, which may not leave the platform; it wasn't sent to the model (the gateway's guardrail)"


# LiteLLM's side: a pre-call guardrail (the chart's llm-config names it: guardrail: llm_guard.StorScaleGuardrail).
try:
    from litellm.integrations.custom_guardrail import CustomGuardrail
except ImportError:   # the stand-in and the checks run without LiteLLM
    CustomGuardrail = object


class StorScaleGuardrail(CustomGuardrail):
    """Refuses a request that carries what may not leave, with HTTP 400 and the refusal's words."""

    def __init__(self, **kwargs):
        super().__init__(**kwargs)

    async def async_pre_call_hook(self, user_api_key_dict, cache, data, call_type):
        # A chat's messages, or the text sent to be embedded (a knowledge bank's passages, a search's question).
        inp = data.get("input")
        texts = [inp] if isinstance(inp, str) else [t for t in (inp or []) if isinstance(t, str)]
        kinds = found((data.get("messages") or []) + [{"content": t} for t in texts])
        if not kinds:
            return data
        from fastapi import HTTPException
        from litellm._logging import verbose_proxy_logger
        verbose_proxy_logger.warning("storscale guardrail refused a request on key %s: %s", getattr(user_api_key_dict, "key_alias", None), ", ".join(kinds))
        raise HTTPException(status_code=400, detail={"error": refusal(kinds), "type": "guardrail"})
