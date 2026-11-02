# Checkpoint TLS behind Hula

Checkpoint receipts use `EXPORTER-Mitosu-Checkpoint-v2` with a 32-byte TLS
exporter. A reverse proxy terminates one TLS connection and opens another, so
exporting the backend session cannot verify against the frontend client session.

`upstream_tls` optionally presents a client certificate to a verified HTTPS
backend. On the dedicated `/checkpoints` route, `checkpoint_binding: true`
additionally derives the frontend exporter and forwards it as
`X-Mitosu-Checkpoint-Binding: v1.<64 lowercase hex digits>`. All configured plain
proxy routes remove incoming values of that reserved header. Binding injection
runs after hop-by-hop header removal, so a supplied `Connection` header cannot
remove or replace the real binding. The binding is not logged.

```yaml
proxies:
  - target: https://relay:8002
    by_domain: cloud.mitosu.io
    by_path: /multipoint
    upstream_tls:
      client_cert: /etc/hula/checkpoint-proxy/client.crt
      client_key: /etc/hula/checkpoint-proxy/client.key
  - target: https://relay:8002
    by_domain: cloud.mitosu.io
    by_path: /checkpoints
    upstream_tls:
      client_cert: /etc/hula/checkpoint-proxy/client.crt
      client_key: /etc/hula/checkpoint-proxy/client.key
      checkpoint_binding: true
```

The relay must explicitly enable proxy binding and require verified client
certificates from a dedicated proxy CA. That CA conveys proxy authority; do not
reuse a CA that issues ordinary client certificates. Keep the existing relay
server certificate verification. All routes to a relay requiring mTLS need the
upstream client pair, including the existing `/multipoint` route.

There is no plaintext or unverified-TLS fallback. Binding routes require HTTPS,
a valid client key pair, a host-specific `/checkpoints` match, an incoming TLS
WebSocket upgrade, and successful exporter derivation. Ordinary proxy behavior
and direct relay connections remain unchanged when this option is absent.

The client continues to verify its original frontend TLS session, request nonce,
account, room, request contents, and receipt MAC. Notes encryption and signed
catalog/device approval remain separate and unchanged.

## Validation

`go test ./server -run TestCheckpointTLS -count=1` exercises two actual verified
TLS hops, backend client-certificate verification, distinct frontend connections,
a forged binding and hop-by-hop header, and receipt MACs matching the frontend
rather than the backend exporter. It also rejects cleartext, disabled upstream
verification, and public binding headers on ordinary routes. Existing proxy,
routing, and gRPC regression tests pass.

This patch does not alter a running service or create production certificates.
Roll out the paired relay change and test with a disposable account before
activating returning-folder or legacy-migration admission.
