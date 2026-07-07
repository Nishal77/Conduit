#!/usr/bin/env python3
"""Example HTTP callback plugin implementing spec/14-plugins.md's protocol.

Demonstrates that a Conduit plugin needs no Go code at all: any language
that can run an HTTP server can implement the Before/After hook contract.
This one redacts email addresses from tool call arguments before they
reach the upstream MCP server — a Python-side equivalent of the built-in
pii-redactor plugin, minus the other four PII patterns.

Run:
    python3 server.py

Register it with Conduit (see README.md alongside this file for the full
tenant_plugins payload):
    PUT /api/v1/tenants/{tenantID}/plugins/{pluginID}
    {"enabled": true, "config": {"before_url": "http://localhost:8090/before"}}
"""

import hashlib
import hmac
import json
import re
from http.server import BaseHTTPRequestHandler, HTTPServer

EMAIL_PATTERN = re.compile(r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b")

# Set this to the same value configured in tenant_plugins.config.secret to
# verify Conduit's X-Conduit-Signature header. Leave empty to skip
# verification (fine for local testing, never for production).
SHARED_SECRET = ""


def verify_signature(secret: str, body: bytes, signature: str) -> bool:
    """Mirrors spec/16-webhooks.md §5's verification example — the plugin
    callback protocol uses the same sha256=<hex hmac> scheme as webhooks."""
    expected = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, signature)


def redact(value):
    """Recursively walks a JSON value, redacting emails found in strings."""
    if isinstance(value, str):
        return EMAIL_PATTERN.sub("[EMAIL]", value)
    if isinstance(value, dict):
        return {k: redact(v) for k, v in value.items()}
    if isinstance(value, list):
        return [redact(v) for v in value]
    return value


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)

        if SHARED_SECRET:
            signature = self.headers.get("X-Conduit-Signature", "")
            if not verify_signature(SHARED_SECRET, body, signature):
                self.send_response(401)
                self.end_headers()
                return

        request = json.loads(body)

        if self.path == "/before":
            self.handle_before(request)
        elif self.path == "/after":
            self.handle_after(request)
        else:
            self.send_response(404)
            self.end_headers()

    def handle_before(self, request):
        params = request.get("params") or {}
        arguments = params.get("arguments")
        if arguments is not None:
            params = dict(params, arguments=redact(arguments))

        response = {
            "action": "allow",
            "request": {"method": request.get("method"), "params": params},
        }
        self.write_json(response)

    def handle_after(self, request):
        # This example only redacts outbound arguments — pass responses
        # through unchanged. A real plugin could inspect request["response"]
        # here and return a modified one the same way handle_before does.
        self.write_json({"response": request.get("response")})

    def write_json(self, payload):
        body = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass  # quiet by default; remove to see request logs


if __name__ == "__main__":
    HTTPServer(("localhost", 8090), Handler).serve_forever()
