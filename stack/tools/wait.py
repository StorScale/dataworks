"""Wait, before a service starts in Kubernetes, as Compose's depends_on does.

    python wait.py url <url> [<url> ...]   until each answers (any HTTP status below 500);
                                            <url>=~<text>: and its answer contains <text>
    python wait.py job <step> [<step> ...]  until each setup step has finished for this
                                            revision (STORSCALE_REVISION) or a later one
    python wait.py secret <name> <key>      until the Secret has the key
    python wait.py tcp <host:port> [...]    until each accepts connections (a database)
"""
import socket
import sys
import time

import requests

import k8s


def ready(kind, arg):
    try:
        if kind == "url":
            url, _, text = arg.partition("=~")
            r = requests.get(url, timeout=5, verify=False)
            return r.status_code < 500 and text in r.text
        if kind == "job":
            return k8s.done_revision(arg) >= int(k8s.os.environ["STORSCALE_REVISION"])
        if kind == "secret":
            name, key = arg.split("/", 1)
            return bool(k8s.get_secret(name).get(key))
        if kind == "tcp":
            host, _, port = arg.rpartition(":")
            socket.create_connection((host, int(port)), timeout=5).close()
            return True
    except Exception:  # noqa: BLE001 (not there yet)
        return False
    raise SystemExit(__doc__)


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    kind, args = sys.argv[1], sys.argv[2:]
    if kind == "secret":
        args = [f"{args[0]}/{args[1]}"]
    requests.packages.urllib3.disable_warnings()
    started = time.time()
    for arg in args:
        while not ready(kind, arg):
            if int(time.time() - started) % 30 == 0:
                print(f"waiting for {kind} {arg}", flush=True)
            time.sleep(2)
        print(f"ready: {kind} {arg} ({time.time() - started:.0f}s)", flush=True)


if __name__ == "__main__":
    main()
