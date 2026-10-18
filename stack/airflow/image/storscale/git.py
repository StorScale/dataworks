"""A project's repo, checked out with its git connection (git_<project>):
an https repo's token as a header, never in a URL or on disk; an ssh repo's
key in a file only its run reads, removed after."""
import base64
import json
import os
import subprocess
import tempfile


def checkout(conn, ref, work, depth=50):
    """The repo at ref (a commit, a tag, a branch), in work; its commit."""
    env = {**os.environ, "GIT_TERMINAL_PROMPT": "0"}
    auth, keyfile = [], None
    extra = json.loads(conn.extra or "{}") if conn.extra else {}
    try:
        if extra.get("private_key"):
            if extra.get("private_key_passphrase"):
                raise ValueError("an ssh key with a passphrase: the platform can't check it out")
            fd, keyfile = tempfile.mkstemp(prefix="key-")
            with os.fdopen(fd, "w") as f:
                f.write(extra["private_key"].rstrip("\n") + "\n")
            hosts = (f"-o StrictHostKeyChecking=yes -o UserKnownHostsFile={extra['known_hosts_file']}"
                     if extra.get("known_hosts_file") else "-o StrictHostKeyChecking=accept-new")
            env["GIT_SSH_COMMAND"] = f"ssh -i {keyfile} -o IdentitiesOnly=yes {hosts}"
        elif conn.password:
            basic = base64.b64encode(f"{conn.login or 'oauth2'}:{conn.password}".encode()).decode()
            auth = ["-c", f"http.extraHeader=Authorization: Basic {basic}"]
        subprocess.run(["git", "init", "-q", work], check=True, env=env)
        subprocess.run(["git", *auth, "-C", work, "fetch", "-q", "--depth", str(depth), "--", conn.host, ref],
                       check=True, env=env, capture_output=True, text=True)
        subprocess.run(["git", "-C", work, "checkout", "-q", "FETCH_HEAD"], check=True, env=env)
        return subprocess.run(["git", "-C", work, "rev-parse", "HEAD"], check=True, env=env,
                              capture_output=True, text=True).stdout.strip()
    finally:
        if keyfile:
            os.remove(keyfile)
