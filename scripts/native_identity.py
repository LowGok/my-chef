"""Exact package selector for disposable native probes; no provider operations."""
import os


def application_id():
    lane = os.environ.get("NATIVE_IDENTITY_LANE", "preview")
    if lane == "preview":
        return "com.gkldevelopment.mychef.preview"
    if lane == "app":
        return "dev.mychef.app"
    raise ValueError("Unknown native identity lane")
