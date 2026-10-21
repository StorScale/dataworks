
# VS Code in the browser (use case 7.8): code-server behind jupyter-server-proxy at the server's /vscode/,
# on the person's files, with no sign-in of its own (the hub has signed them in).
c.ServerProxy.servers = {  # noqa: F821 (c is the config object the server gives this file)
    "vscode": {
        "command": ["code-server", "--auth", "none", "--bind-addr", "127.0.0.1:{port}", "--disable-telemetry", "--disable-update-check", "/home/jovyan/work"],
        "timeout": 60,
        "launcher_entry": {"title": "VS Code", "enabled": True},
    },
}
