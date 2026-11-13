"""Seed a disposable verified account through the app's real fixture API."""
from contextlib import closing
import http.client
import json
import re
import secrets
import time


def seed(port, origin, capability, username="nativefixture"):
    if port != 18137 or not re.fullmatch(r"[a-z][a-z0-9]{3,24}", username):
        raise ValueError("Expected an owned loopback fixture and a synthetic username")
    if origin != "https://localhost:8977":
        raise ValueError("Expected a private native fixture origin")
    if not re.fullmatch(r"[A-Za-z0-9_-]{43}", capability):
        raise ValueError("Expected a disposable fixture inbox capability")
    profile = dict(firstName="Native", lastName="Fixture", username=username, email=username + "@example.test")
    password = "GardenNative" + secrets.token_hex(8)

    def call(method, route, data=None, headers=None):
        with closing(http.client.HTTPConnection("127.0.0.1", port, timeout=8)) as connection:
            connection.request(method, "/api/v1/" + route, json.dumps(data) if data is not None else None,
                               {"Content-Type": "application/json", "Origin": origin, **(headers or {})})
            response = connection.getresponse()
            body = response.read(65537)
            if len(body) > 65536:
                raise RuntimeError("Fixture response exceeded the size bound")
            return response.status, json.loads(body) if body else {}, response.getheader("Set-Cookie")

    status, session, cookie = call("GET", "session")
    if status != 200 or not cookie or not session.get("csrfToken"):
        raise RuntimeError("Fixture session could not be created")
    auth = {"Cookie": cookie.split(";", 1)[0], "X-CSRF-Token": session["csrfToken"]}
    if call("POST", "register", profile, auth)[0] != 202:
        raise RuntimeError("Fixture registration could not be started")
    fixture = {"Authorization": "Bearer " + capability}
    deadline = time.monotonic() + 15
    proof = None
    while time.monotonic() < deadline:
        status, mail, _ = call("POST", "fixture/inbox", {"recipient": profile["email"]}, fixture)
        if status == 200:
            proof = re.search(r"/app/signup#([A-Za-z0-9_-]{43})", mail.get("text", ""))
            if proof:
                break
        elif status != 204:
            raise RuntimeError("Fixture inbox declined the account setup")
        time.sleep(0.5)
    if not proof:
        raise RuntimeError("Fixture verification message did not arrive")
    complete = {k: profile[k] for k in ("firstName", "lastName", "username")}
    complete.update(proof=proof.group(1), password=password)
    if call("POST", "signup/complete", complete, auth)[0] != 201:
        raise RuntimeError("Fixture account could not be verified")
    status, user, _ = call("POST", "login", {"email": profile["email"], "password": password}, auth)
    if status != 200 or not user.get("user"):
        raise RuntimeError("Verified fixture account could not sign in")
    account = user["user"]
    route = "fixture/personal-recipes/entitlement"
    status, lease, _ = call("GET", route, headers=fixture)
    generation = lease.get("generation", "")
    if status != 200 or not re.fullmatch(r"[0-9a-f]{32}", generation):
        raise RuntimeError("Fixture entitlement generation is unavailable")
    if call("POST", route, dict(accountId=account["id"], accountInstance=account["accountInstance"],
                                state="premium", generation=generation, operationId=secrets.token_hex(16)), fixture)[0] != 204:
        raise RuntimeError("Fixture Premium access could not be granted")
    return {"email": profile["email"], "password": password}
