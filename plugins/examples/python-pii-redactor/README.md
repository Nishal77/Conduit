# Example: Python HTTP callback plugin

A minimal Conduit plugin written in Python, implementing the HTTP callback
protocol from `spec/14-plugins.md` §3. It redacts email addresses from tool
call arguments before they reach the upstream MCP server.

This is the reference example for "write a plugin in any language" — no Go
code, no SDK, no dependency beyond the Python standard library.

## Run it

```bash
python3 server.py
# listening on http://localhost:8090
```

## Register it with Conduit

First, register it as a catalog entry if you haven't already (built-in
plugins are seeded by migration 000004; an HTTP callback plugin like this
one is registered the same way, once, by an operator — not by every
tenant):

```bash
curl -X POST http://localhost:8081/api/v1/plugins \
  -H "Authorization: Bearer $CONDUIT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "python-pii-redactor",
    "version": "1.0.0",
    "plugin_type": "http_callback",
    "description": "Example Python plugin redacting emails from tool call arguments"
  }'
```

Then enable it for a tenant:

```bash
curl -X PUT http://localhost:8081/api/v1/tenants/$TENANT_ID/plugins/$PLUGIN_ID \
  -H "Authorization: Bearer $CONDUIT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "priority": 100,
    "config": {
      "before_url": "http://localhost:8090/before"
    }
  }'
```

Conduit's plugin loader (`cmd/conduit/plugins.go`) picks up the change on
its next refresh (within 5 seconds) and starts POSTing every `tools/call`
request to `http://localhost:8090/before` before forwarding it upstream.

## Verifying signatures

If you set a `secret` in the tenant_plugins config, Conduit signs every
callback request with `X-Conduit-Signature: sha256=<hex hmac>`. Set
`SHARED_SECRET` in `server.py` to the same value to verify it — see
`verify_signature()`.
