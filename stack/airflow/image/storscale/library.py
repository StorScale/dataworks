"""The company's shared libraries in a project's DAGs (use case U.2, R-U.4):
code kept once, in a repo the administrators name on Administration →
Templates, and used by every project without copying it.

    from storscale import library

    @task
    def score():
        library.use("company-utils")          # its repo's files on sys.path, at its ref
        from company_utils import clean
        ...

A dbt project names a library in its packages.yml by its repo's address (as
Administration → Templates shows it), and storscale.dbt.build fetches it:

    packages:
      - git: "https://gitlab.example.com/data/libraries/company-dbt.git"

The platform reads the library with the install's token, as the project's
pipelines ask it, so the project holds no token for it. Each commit is
fetched once per worker (a cache under /tmp, by commit).
"""
import io
import os
import re
import shutil
import sys
import tarfile
import tempfile

import requests

PLATFORM = os.environ.get("STORSCALE_PLATFORMD_URL", "http://platformd:8080").rstrip("/")
CACHE = os.environ.get("STORSCALE_LIBRARIES", os.path.join(tempfile.gettempdir(), "storscale-libraries"))


def _token():
    from storscale.dbt import token
    return token()


def _get(path, token, **kw):
    r = requests.get(f"{PLATFORM}{path}", headers={"Authorization": f"Bearer {token}"}, timeout=300, **kw)
    if r.status_code != 200:
        try:
            why = r.json().get("error", r.text[:200])
        except ValueError:
            why = r.text[:200]
        raise RuntimeError(f"storscale.library: {path}: HTTP {r.status_code}: {why}")
    return r


def libraries(token=None):
    """The libraries, as the platform lists them: name, description, repo, ref and url."""
    return _get("/api/libraries", token or _token()).json()["libraries"]


def fetch(name, token=None, cache=CACHE):
    """The folder holding a library's files at the commit its ref names now."""
    token = token or _token()
    lib = _get(f"/api/libraries/{name}", token).json()
    commit = lib["commit"]
    if not re.fullmatch(r"[0-9a-f]{7,64}", commit):
        raise RuntimeError(f"storscale.library: {name}: the platform named no commit")
    where = os.path.join(cache, f"{name}-{commit[:16]}")
    if os.path.isdir(where):
        return where
    data = _get(f"/api/libraries/{name}/archive?commit={commit}", token).content
    os.makedirs(cache, exist_ok=True)
    work = tempfile.mkdtemp(dir=cache, prefix=f".{name}-")
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as tar:
        tar.extractall(work, filter="data")   # no links out, no absolute paths
    tops = os.listdir(work)
    if len(tops) != 1:
        raise RuntimeError(f"storscale.library: {name}: an archive with {len(tops)} top folders")
    try:
        os.rename(os.path.join(work, tops[0]), where)
    except OSError:   # another task fetched it first
        if not os.path.isdir(where):
            raise
    shutil.rmtree(work, ignore_errors=True)
    print(f"storscale.library: {name} at {commit[:12]} ({lib['repo']} {lib['ref']})")
    return where


def use(name, path="", token=None):
    """A library's files on sys.path (its folder path in the repo, default its
    top): its modules import as any other. The folder is returned."""
    where = os.path.normpath(os.path.join(fetch(name, token), path))
    if where not in sys.path:
        sys.path.insert(0, where)
    return where


def _plain(url):
    """A repo's address, as the libraries' are compared: no credentials, no .git, no trailing slash."""
    url = re.sub(r"^(https?://)[^@/]+@", r"\1", url.strip())
    return re.sub(r"(\.git)?/?$", "", url).lower()


def localize(packages, token=None):
    """A dbt packages.yml's entries (a list of dicts), each git one naming a
    library's repo replaced by its files, fetched (local: <folder>); the
    others as they were. The libraries replaced are returned too."""
    wanted = [p for p in packages if isinstance(p, dict) and "git" in p]
    if not wanted:
        return packages, []
    token = token or _token()
    by_url = {_plain(lib["url"]): lib["name"] for lib in libraries(token)}
    out, used = [], []
    for p in packages:
        name = by_url.get(_plain(p["git"])) if isinstance(p, dict) and "git" in p else None
        if name is None:
            out.append(p)
            continue
        if p.get("revision"):
            print(f"storscale.library: {name}: the library's ref is the administrators' (revision {p['revision']} ignored)")
        out.append({"local": os.path.normpath(os.path.join(fetch(name, token), p.get("subdirectory", "")))})
        used.append(name)
    return out, used
