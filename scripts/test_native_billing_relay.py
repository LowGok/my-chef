"""Rehearse simulated billing through the installed harness's local HTTPS relay."""
from contextlib import ExitStack, closing
from datetime import datetime, timedelta, timezone
import base64
import hmac
import http.client
import json
import os
from pathlib import Path
import re
import ssl
import tempfile
import uuid

from native_fixture_account import fixture_billing_key, seed
from test_native_installed import ORIGIN, fixture


def synthetic_receipt(capability, store, binding):
    if not re.fullmatch(r"[a-f0-9-]{36}", binding):
        raise ValueError("Unexpected simulated receipt input")
    now = datetime.now(timezone.utc)
    key = fixture_billing_key(capability, store)
    order = str(uuid.uuid4())
    body = json.dumps({
        "Binding": binding, "Order": order, "Interval": "monthly",
        "StartsAt": (now - timedelta(seconds=1)).isoformat(),
        "PaidThrough": (now + timedelta(minutes=5)).isoformat(),
    }, separators=(",", ":")).encode()

    def encode(value):
        return base64.urlsafe_b64encode(value).rstrip(b"=").decode()

    return "fixture." + encode(body) + "." + encode(hmac.digest(key, body, "sha256"))


def check_relay(state, ca):
    context = ssl.create_default_context(cafile=ca)
    capability = json.loads((state / "fixture-inbox.json").read_text())["capability"]

    def call(method, route, status, payload=None, access=None):
        headers = {"Content-Type": "application/json"}
        if access:
            headers["Authorization"] = "Bearer " + access
        with closing(http.client.HTTPSConnection("localhost", 8977, timeout=8, context=context)) as conn:
            conn.request(method, "/api/native/v1/" + route, json.dumps(payload) if payload is not None else None, headers)
            response = conn.getresponse()
            body = response.read(65537)
            if response.status != status or len(body) > 65536:
                # Print statuses only. Route details can include an opaque checkout ID.
                raise RuntimeError(f"Simulated billing relay expected {status}, got {response.status}")
            return json.loads(body) if body else {}

    for platform, store in (("ios", "apple"), ("android", "play")):
        account = seed(18137, ORIGIN, capability, "billingfixture" + platform, premium=False)

        def sign_in():
            access = call("POST", "session", 201, {"platform": platform})["native"]["accessToken"]
            return call("POST", "login", 200, account, access)["native"]["accessToken"]

        access = sign_in()
        if call("GET", "personal-recipes/permissions", 200, access=access)["canWrite"]:
            raise RuntimeError("Simulated billing account did not start Free")
        offer = call("GET", "billing/status", 200, access=access)
        if (offer["store"] != store or not offer["checkoutEnabled"] or not offer["restoreAvailable"]
                or {plan["interval"] for plan in offer["plans"]} != {"monthly", "annual"}):
            raise RuntimeError("Simulated billing plans were unavailable")
        checkout = call("POST", "billing/checkouts", 201, {"interval": "monthly"}, access)
        route = "billing/checkouts/" + checkout["id"]
        call("POST", route + "/launch", 204, access=access)
        receipt = synthetic_receipt(capability, store, checkout["binding"])
        if store == "apple":
            call("POST", route + "/verify", 404, {"receipt": receipt + "x"}, access)
            completion = call("POST", route + "/verify", 200, {"receipt": receipt}, access)
            if not completion["completeStoreTransaction"]:
                raise RuntimeError("Verified checkout did not permit completion")
        access = sign_in()
        # For Play, omit the original result entirely and restore from the same
        # account's new session. For Apple, confirm an already fulfilled receipt.
        call("POST", "billing/restore", 200, {"receipt": receipt}, access)
        if not call("GET", "personal-recipes/permissions", 200, access=access)["canWrite"]:
            raise RuntimeError("Simulated billing did not restore Premium")
    print("Simulated Apple checkout and Play lost-result restore passed over the installed TLS relay.")


def main():
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="mychef-billing-relay-") as directory, ExitStack() as stack:
        state = Path(directory).resolve()
        ca, _, _ = fixture(state, stack, billing=True)
        check_relay(state, ca)


if __name__ == "__main__":
    main()
