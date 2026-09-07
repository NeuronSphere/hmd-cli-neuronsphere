"""Argo step entrypoint: call the bender-sessions service to start/stop a session.

Reads METHOD, URL, BODY from env. On a successful POST, writes
``/tmp/session_id``, ``/tmp/session_url``, and ``/tmp/session_token`` files so
Argo can collect them as step outputs. DELETE writes nothing. Any non-2xx is a
hard failure that fails the step.

Intentionally minimal — no auth headers, no retries beyond a single try. The
session manager is the trust boundary on the receiving side; this caller runs
inside the deploy workflow's network and is reached over plain HTTPS.
"""

import json
import os
import sys
import urllib.request


def main() -> int:
    method = os.environ["METHOD"].upper()
    url = os.environ["URL"]
    body = os.environ.get("BODY") or ""

    data = body.encode() if body and method != "DELETE" else None
    req = urllib.request.Request(
        url,
        data=data,
        method=method,
        headers={"Content-Type": "application/json"} if data else {},
    )
    with urllib.request.urlopen(req, timeout=60) as resp:
        payload = resp.read().decode()

    if method == "POST":
        parsed = json.loads(payload)
        with open("/tmp/session_id", "w") as fl:
            fl.write(parsed["session_id"])
        with open("/tmp/session_url", "w") as fl:
            fl.write(parsed["session_url"])
        with open("/tmp/session_token", "w") as fl:
            fl.write(parsed["token"])
    print(payload)
    return 0


if __name__ == "__main__":
    sys.exit(main())
