#!/usr/bin/env python3
"""Seed the throwaway Harbor of the dev stack and the tests.

devenv/harbor/harbor.sh runs it in two steps, with the image pushes between
them, and it is safe to run again:

  seed.py projects   projects (with a quota and scan-on-push) before any push
  seed.py finish     everything that needs the images: an extra tag, waiting
                     for the vulnerability scans, a replication run, a garbage
                     collection run, and the robot accounts for Grafana

"finish" writes the robot accounts' secrets to SEED_OUT:

  secret           robot$grafana: every permission the plugin can use
  limited-secret   robot$grafana-limited: may only list projects,
                   repositories and artifacts

Only the standard library is used, so any Python image can run it.
"""

import base64
import json
import os
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

URL = os.environ.get("HARBOR_URL", "http://proxy:8080").rstrip("/")
ADMIN_PASSWORD = os.environ.get("HARBOR_ADMIN_PASSWORD", "Harbor-dev-0nly")
OUT = os.environ.get("SEED_OUT", "/seed-out")
API = URL + "/api/v2.0"

MB = 1024 * 1024

# name -> (public, storage quota in bytes or -1 for unlimited)
PROJECTS = {
    "platform": (False, 500 * MB),
    "apps": (False, -1),
    "sandbox": (True, 50 * MB),
}

# What the robot account for Grafana may do. Nothing here changes anything.
READER_SYSTEM = [
    ("audit-log", "list"),
    ("catalog", "read"),
    ("garbage-collection", "list"),
    ("garbage-collection", "read"),
    ("jobservice-monitor", "list"),
    ("project", "list"),
    ("quota", "list"),
    ("quota", "read"),
    ("registry", "list"),
    ("registry", "read"),
    ("replication", "list"),
    ("replication", "read"),
    ("replication-policy", "list"),
    ("replication-policy", "read"),
    ("robot", "list"),
    ("robot", "read"),
    ("scan-all", "read"),
    ("scanner", "list"),
    ("scanner", "read"),
    ("security-hub", "list"),
    ("security-hub", "read"),
    ("system-volumes", "read"),
]
READER_PROJECT = [
    ("accessory", "list"),
    ("artifact", "list"),
    ("artifact", "read"),
    ("artifact-addition", "read"),
    ("log", "list"),
    ("metadata", "list"),
    ("metadata", "read"),
    ("project", "read"),
    ("quota", "read"),
    ("repository", "list"),
    ("repository", "read"),
    ("scan", "read"),
    ("tag", "list"),
]
LIMITED_SYSTEM = [("project", "list")]
LIMITED_PROJECT = [("project", "read"), ("repository", "list"), ("artifact", "list"), ("tag", "list")]


def call(method, path, body=None):
    """Return (status, parsed body or text). HTTP errors are returned, not raised."""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(API + path, data=data, method=method)
    token = base64.b64encode(f"admin:{ADMIN_PASSWORD}".encode()).decode()
    req.add_header("Authorization", "Basic " + token)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            status, raw = resp.status, resp.read()
    except urllib.error.HTTPError as err:
        status, raw = err.code, err.read()
    try:
        return status, json.loads(raw) if raw else None
    except ValueError:
        return status, raw.decode(errors="replace")


def must(method, path, body=None, ok=(200, 201)):
    status, answer = call(method, path, body)
    if status not in ok:
        sys.exit(f"seed: {method} {path} answered {status}: {answer}")
    return answer


def wait_healthy():
    for _ in range(120):
        status, answer = call("GET", "/health")
        if status == 200 and isinstance(answer, dict) and answer.get("status") == "healthy":
            return
        time.sleep(5)
    sys.exit("seed: Harbor did not become healthy")


def seed_projects():
    for name, (public, quota) in PROJECTS.items():
        status, _ = call(
            "POST",
            "/projects",
            {
                "project_name": name,
                "storage_limit": quota,
                # Scan every image when it is pushed.
                "metadata": {"public": str(public).lower(), "auto_scan": "true"},
            },
        )
        if status not in (201, 409):
            sys.exit(f"seed: creating project {name} answered {status}")
    # The project Harbor creates itself scans on push too.
    must("PUT", "/projects/library", {"metadata": {"public": "true", "auto_scan": "true"}}, ok=(200,))
    print("seed: projects ready")


def access(pairs):
    return [{"resource": resource, "action": action} for resource, action in pairs]


def seed_robot(name, system, project, out_file):
    """Create the robot account, or give an existing one a new secret."""
    secret = "Rb1" + secrets.token_urlsafe(24)
    permissions = [
        {"kind": "system", "namespace": "/", "access": access(system)},
        {"kind": "project", "namespace": "*", "access": access(project)},
    ]
    body = {
        "name": name,
        "description": "Read-only account for the Grafana data source (dev stack)",
        "duration": -1,
        "level": "system",
        "disable": False,
        "permissions": permissions,
    }
    full_name = "robot$" + name
    existing = must("GET", "/robots?page_size=100&q=" + urllib.parse.quote(f"name={name}"), ok=(200,))
    robot = next((r for r in existing if r.get("name") == full_name), None)
    if robot is None:
        robot = must("POST", "/robots", body, ok=(201,))
    else:
        must("PUT", f"/robots/{robot['id']}", {**body, "id": robot["id"], "name": full_name}, ok=(200,))
    must("PATCH", f"/robots/{robot['id']}", {"secret": secret}, ok=(200,))
    with open(os.path.join(OUT, out_file), "w", encoding="utf-8") as handle:
        handle.write(secret)
    os.chmod(os.path.join(OUT, out_file), 0o644)
    print(f"seed: robot account {full_name} ready")


def seed_tag():
    """A second tag on one artifact, so an artifact with several tags exists."""
    status, _ = call("POST", "/projects/platform/repositories/alpine/artifacts/3.20/tags", {"name": "stable"})
    if status not in (201, 409):
        sys.exit(f"seed: tagging platform/alpine:3.20 answered {status}")


def wait_for_scans():
    """Wait until Trivy has scanned the image that is known to have findings."""
    path = "/projects/platform/repositories/alpine/artifacts/3.10?with_scan_overview=true"
    last = "not started"
    asked = False
    for attempt in range(120):
        status, artifact = call("GET", path)
        if status != 200:
            sys.exit(f"seed: platform/alpine:3.10 is missing ({status}): push the images first")
        overview = next(iter((artifact.get("scan_overview") or {}).values()), None)
        last = (overview or {}).get("scan_status", "not started")
        if last == "Success":
            print("seed: vulnerability scans finished")
            return
        stuck = attempt >= 48 and last in ("Pending", "Running", "Scheduled")
        if not asked and (last in ("Error", "Stopped") or stuck or (overview is None and attempt >= 6)):
            # Scan-on-push did not run, failed (often the first download of
            # Trivy's database) or hangs: ask once more.
            if stuck:
                call("POST", "/projects/platform/repositories/alpine/artifacts/3.10/scan/stop", {})
                time.sleep(5)
            call("POST", "/projects/platform/repositories/alpine/artifacts/3.10/scan", {})
            asked = True
        time.sleep(5)
    sys.exit(f"seed: the scan of platform/alpine:3.10 did not finish (last status: {last})")


def seed_replication():
    """A replication rule that copies library/** into the project "mirror" of
    this same Harbor, run once, so that replication executions exist."""
    registries = must("GET", "/registries?page_size=100", ok=(200,))
    registry = next((r for r in registries if r["name"] == "self"), None)
    if registry is None:
        must(
            "POST",
            "/registries",
            {
                "name": "self",
                "type": "harbor",
                "url": URL,
                "insecure": True,
                "credential": {"type": "basic", "access_key": "admin", "access_secret": ADMIN_PASSWORD},
            },
            ok=(201,),
        )
        registries = must("GET", "/registries?page_size=100", ok=(200,))
        registry = next(r for r in registries if r["name"] == "self")

    policies = must("GET", "/replication/policies?page_size=100", ok=(200,))
    policy = next((p for p in policies if p["name"] == "mirror-library"), None)
    if policy is None:
        must(
            "POST",
            "/replication/policies",
            {
                "name": "mirror-library",
                "description": "Copies library/** into the project mirror (dev stack)",
                "dest_registry": {"id": registry["id"]},
                "dest_namespace": "mirror",
                "dest_namespace_replace_count": 1,
                "filters": [{"type": "name", "value": "library/**"}],
                "trigger": {"type": "manual"},
                "enabled": True,
                "override": True,
                "speed": -1,
            },
            ok=(201,),
        )
        policies = must("GET", "/replication/policies?page_size=100", ok=(200,))
        policy = next(p for p in policies if p["name"] == "mirror-library")

    executions = must("GET", f"/replication/executions?policy_id={policy['id']}", ok=(200,))
    if not executions:
        must("POST", "/replication/executions", {"policy_id": policy["id"]}, ok=(201,))
    for _ in range(60):
        executions = must("GET", f"/replication/executions?policy_id={policy['id']}", ok=(200,))
        if executions and executions[0].get("status") in ("Succeed", "Failed", "Stopped"):
            print(f"seed: replication run finished: {executions[0]['status']}")
            return
        time.sleep(3)
    print("seed: the replication run is still going; carrying on")


def seed_gc():
    """One dry run of garbage collection, so that its history is not empty."""
    history = must("GET", "/system/gc?page_size=1", ok=(200,))
    if history:
        return
    must(
        "POST",
        "/system/gc/schedule",
        {"schedule": {"type": "Manual"}, "parameters": {"delete_untagged": False, "dry_run": True, "workers": 1}},
        ok=(201,),
    )
    for _ in range(40):
        history = must("GET", "/system/gc?page_size=1", ok=(200,))
        if history and history[0].get("job_status") in ("Success", "Error", "Stopped"):
            break
        time.sleep(3)
    print("seed: garbage collection dry run done")


def main():
    step = sys.argv[1] if len(sys.argv) > 1 else ""
    wait_healthy()
    if step == "projects":
        seed_projects()
    elif step == "finish":
        os.makedirs(OUT, exist_ok=True)
        seed_tag()
        wait_for_scans()
        seed_replication()
        seed_gc()
        seed_robot("grafana", READER_SYSTEM, READER_PROJECT, "secret")
        seed_robot("grafana-limited", LIMITED_SYSTEM, LIMITED_PROJECT, "limited-secret")
        print("seed: done")
    else:
        sys.exit("usage: seed.py projects|finish")


if __name__ == "__main__":
    main()
