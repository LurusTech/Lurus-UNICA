#!/usr/bin/env python3
"""Create one Dify app per UNICA product line and issue its service API key.

Idempotent: an app whose name already exists is reused rather than duplicated,
and an existing key is reused rather than piling up new ones. That matters
because the script is the recovery path after a stack is rebuilt, and running it
twice must not leave a workspace full of near-identical apps.

Usage (inside WSL, with the stack running):
    DIFY_BASE=http://localhost:3402 \
    DIFY_EMAIL=admin@unica.local DIFY_PASSWORD=... \
    python3 bootstrap_apps.py MegaStore FreshMart TechZone
"""

import json
import os
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("DIFY_BASE", "http://localhost:3402").rstrip("/")
EMAIL = os.environ.get("DIFY_EMAIL", "admin@unica.local")
PASSWORD = os.environ.get("DIFY_PASSWORD", "")
API = BASE + "/console/api"

# Prefix keeps UNICA's apps identifiable in a workspace that may also hold a
# customer's own experiments.
PREFIX = "UNICA-"


def request(method, path, token=None, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(API + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.read().decode()
    except urllib.error.HTTPError as exc:
        raise SystemExit(f"{method} {path} -> HTTP {exc.code}: {exc.read().decode()[:300]}")
    return json.loads(raw) if raw else {}


def login():
    if not PASSWORD:
        raise SystemExit("DIFY_PASSWORD is not set")
    resp = request("POST", "/login", body={"email": EMAIL, "password": PASSWORD})
    token = (resp.get("data") or {}).get("access_token")
    if not token:
        raise SystemExit(f"login failed: {resp}")
    return token


def find_app(token, name):
    page = request("GET", "/apps?page=1&limit=100", token=token)
    for app in page.get("data", []):
        if app.get("name") == name:
            return app
    return None


def ensure_key(token, app_id):
    existing = request("GET", f"/apps/{app_id}/api-keys", token=token).get("data", [])
    if existing:
        return existing[0].get("token"), "reused"
    return request("POST", f"/apps/{app_id}/api-keys", token=token).get("token"), "created"


def main(lines):
    token = login()
    result = {}

    for line in lines:
        name = PREFIX + line
        app = find_app(token, name)
        if app:
            action = "reused"
        else:
            app = request("POST", "/apps", token=token, body={
                "name": name,
                "mode": "chat",
                "icon": "robot",
                "icon_background": "#FFEAD5",
                "description": f"UNICA 客服应答 - {line}",
            })
            action = "created"

        key, key_action = ensure_key(token, app["id"])
        result[line] = {"app_id": app["id"], "api_key": key}
        print(f"{line:<12} app {action:<7} {app['id']}  key {key_action}: {key[:12]}...")

    out = os.environ.get("OUTPUT", "/tmp/dify_apps.json")
    with open(out, "w") as fh:
        json.dump(result, fh, indent=1, ensure_ascii=False)
    print(f"\nwrote {out}")


if __name__ == "__main__":
    main(sys.argv[1:] or ["MegaStore", "FreshMart", "TechZone"])
