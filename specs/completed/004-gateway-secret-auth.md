---
status: completed
approved: "2026-05-02T19:34:03Z"
generating: "2026-05-02T19:34:07Z"
prompted: "2026-05-02T19:40:40Z"
verifying: "2026-05-02T19:58:48Z"
completed: "2026-05-02T20:40:02Z"
branch: dark-factory/gateway-secret-auth
---

## Summary

- git-rest currently exposes `/api/v1/files/*` with no authentication — any HTTP client with network reach can read/write/delete vault files
- Add optional shared-secret HTTP auth: callers send a secret in a request header, the server rejects mismatches with `401`
- Per-deployment secret, configured via the `GATEWAY_SECRET` env var
- Backward compatible: secret empty/unset → no auth, single startup warning, behavior identical to current

## Problem

The git-rest HTTP API has zero authentication. Any reachable client can call `POST /api/v1/files/<path>` and overwrite or delete vault content. Even when the service runs on an internal-only address (typical Kubernetes `ClusterIP`), the blast radius is every workload that can dial the service. A shared-secret header check is the cheapest meaningful gate that does not require TLS, certificates, or external auth infrastructure.

## Goal

git-rest accepts an optional secret. When set, every request to the file API (`/api/v1/files/*`) must carry the secret in a request header; mismatches are rejected with `401` before any git operation runs. Probe and metrics endpoints (`/healthz`, `/readiness`, `/metrics`) remain unauthenticated so kubelets and Prometheus continue to work. When unset, the service runs identically to the current version and emits a single startup warning.

## Non-goals

- mTLS or per-caller asymmetric auth
- Per-route or per-method authorization (all `/api/v1/*` use one secret)
- Secret rotation at runtime — restart the process with the new value
- IAM / multi-tenant ACLs
- Encrypting traffic — TLS is out of scope; deploy behind a TLS terminator if needed
- Replay protection (nonce / timestamp) — secret is treated as a long-lived static credential

## Header Contract (frozen)

The header names below are part of the public API of this feature. Clients depend on them. Keep them exact.

| Header | Required | Purpose |
|--------|----------|---------|
| `X-Gateway-Secret` | yes (when secret configured) | Must equal the configured secret exactly. Mismatch, missing, or empty → `401`. |
| `X-Gateway-Initator` | yes (when secret configured) | Non-empty caller identity, logged on auth failure. Missing or empty → `500`. (Note: header name is spelt without the second `i` — `Initator`, not `Initiator`. This matches the existing convention in caller code and must not be "corrected".) |

"Missing" and "empty string" are treated identically for both headers. `req.Header.Get("X-Gateway-…")` returning `""` triggers the same response whether the client omitted the header or sent `X-Gateway-…: `.

On a successful auth check, the middleware **strips `X-Gateway-Secret` from the request** (e.g. `req.Header.Del`) before passing it to the inner handler, so the secret never reaches request logs, metrics labels, or downstream code.

## Assumptions

- The HTTP server already exists and mounts a single `gorillamux.NewRouter()` in `createHTTPServer` in `main.go`, currently wrapped by `factory.CreateMetricsMiddleware`.
- The configured listen port is whatever `--listen` / `LISTEN` resolves to — default `:8080`, K8s deployment uses `:9090`. Verification examples below use `9090` purely as illustration; the auth behavior is port-independent.
- Structured logging uses `log/slog` (the rest of `main.go` uses `slog.WarnContext` / `slog.InfoContext`). The startup warning for empty secret follows the same convention.
- Tests use the project's existing test framework (Ginkgo/Gomega with Counterfeiter mocks per `docs/dod.md`).

## Desired Behavior

1. **New CLI flag**: git-rest accepts `--gateway-secret` (`GATEWAY_SECRET`), optional, `display:"length"` so only the secret length appears in startup config logs.
2. **Auth middleware on `/api/v1/*`**: when the configured secret is non-empty, wrap the `/api/v1/*` route subtree (not `/healthz`, `/readiness`, `/metrics`) with a handler that:
   - Returns HTTP `500` with body `header 'X-Gateway-Initator' missing` when `X-Gateway-Initator` is missing or empty.
   - Returns HTTP `401` with body `secret in header 'X-Gateway-Secret' is invalid => access denied` when `X-Gateway-Secret` is missing, empty, or does not equal the configured secret.
   - On success, deletes `X-Gateway-Secret` from the request (`req.Header.Del("X-Gateway-Secret")`) and passes the request to the inner handler.
3. **Probes unauthenticated**: `/healthz`, `/readiness`, `/metrics` remain reachable without any header (kubelet probes and Prometheus scrape have no notion of this secret), with or without secret configured.
4. **Empty-secret mode**: when `--gateway-secret` is empty/unset, no auth middleware is mounted. Exactly one `slog.WarnContext` line is emitted once at startup, containing the substring `gateway-secret not set` and the human-readable explanation `git-rest API is unauthenticated`. This preserves backward compatibility for existing deployments during rollout.
5. **Per-deployment secret**: each git-rest process gets its own secret value. There is no notion of multiple accepted secrets simultaneously; rotation requires a restart.

## Constraints

- **No new external dependencies.** The auth middleware lives entirely inside this repo. Do not import any third-party auth library.
- **Mount point**: the change is local to `createHTTPServer` in `main.go` (the function that builds `gorillamux.NewRouter()` and wraps it with `factory.CreateMetricsMiddleware`). No other file needs structural changes — only the new middleware file and its wiring here.
- **Middleware composition order**: `metrics(auth(api-router)) + probe-router` — auth wraps **only** the `/api/v1/*` subtree, then the existing `factory.CreateMetricsMiddleware` wraps the whole thing. Metrics must still observe `401` and `500` auth responses — auth failures must show up as HTTP metrics with their actual status codes.
- **Probes are NOT wrapped by auth.** `/healthz`, `/readiness`, `/metrics` must remain reachable with no headers. Use a gorilla mux subrouter on `/api/v1` with the auth middleware via `subrouter.Use(...)`, or any composition that achieves the same observable result.
- **Secret comparison is plain string equality.** No constant-time compare in scope (cluster-internal trust model). Revisit if threat model changes.
- **Header names are exact-case.** `X-Gateway-Secret` and `X-Gateway-Initator` (note the deliberate misspelling) are the contract — do not normalise or correct the spelling.
- **HTTP-only** — no TLS work in this spec.
- **Secret never logged.** Use `display:"length"` on the env-tagged struct field so `effective config` log lines show the length, not the value. Strip the secret header before the inner handler so request-log middlewares cannot capture it.

## Failure Modes

| Trigger | Expected Behavior | Recovery |
|---------|-------------------|----------|
| `--gateway-secret` empty / unset | Start with auth disabled, log one `WARNING` line at startup | Set `GATEWAY_SECRET` env to enable auth |
| Request to `/api/v1/*` without `X-Gateway-Initator` | `500` with body `header 'X-Gateway-Initator' missing` | Caller adds initiator header |
| Request to `/api/v1/*` with wrong / missing `X-Gateway-Secret` | `401` with body `secret in header 'X-Gateway-Secret' is invalid => access denied` | Caller fixes / sets secret |
| Request to `/healthz`, `/readiness`, `/metrics` (with or without auth configured) | `200` (probe semantics unchanged), no header check | N/A |
| Caller uses correct headers but operator rotated the secret | `401` until caller restarts with new secret | Restart caller after rotation |
| Multiple secrets accepted (rotation overlap) | Not supported — single secret per process | Schedule a maintenance window |
| Auth middleware panics / misbehaves | `500`; readiness probe still returns `200` (probes not wrapped) | Restart pod |

## Do-Nothing Option

Keep `/api/v1/*` open and rely on network-layer controls (firewall, Kubernetes `NetworkPolicy`, listening only on a localhost socket) to restrict callers. Cheaper — no code change. But:
- Any compromised allowlisted client can still read/write the entire vault end-to-end
- Cross-namespace / cross-host policy management has operational drag
- Doesn't cover port-forwards from operator workstations
- A new caller requires a network policy change instead of a secret distribution

Network-layer controls remain useful in addition to this spec but do not replace it.

## Security / Abuse

- **Secret distribution is out of scope.** This spec assumes operators inject the secret via env var (typically from a secret manager → Kubernetes `Secret` → container env). git-rest does not manage the secret value itself.
- **Secret in env var**: visible via `kubectl exec` and `kubectl get pod -o yaml` to anyone with namespace access. This is the same exposure surface as the existing SSH-key handling — accepted.
- **Logged length only.** `display:"length"` means `effective config` log lines never carry the secret value.
- **No constant-time compare.** Cluster-internal timing attack is implausible given the existing trust model; revisit if threat model changes.
- **Secret header stripped before inner handler.** The existing request-logging / metrics middleware never sees the header, so it cannot leak via Sentry breadcrumbs or structured request logs.
- **No replay protection.** Static long-lived credential. Out of scope.
- **Probe surface stays open.** `/metrics` already reveals operational data without auth — out of scope to lock down here. `/healthz` and `/readiness` are kubelet contracts and stay open by design.

## Acceptance Criteria

Each checkbox is independently verifiable.

**Configuration:**
- [ ] `--gateway-secret` / `GATEWAY_SECRET` flag exists on the `application` struct in `main.go`, optional, with `display:"length"`

**Auth — wrong / missing headers:**
- [ ] With secret configured: request to any `/api/v1/*` route with no `X-Gateway-Initator` header returns HTTP `500`
- [ ] With secret configured: that `500` response body equals exactly `header 'X-Gateway-Initator' missing`
- [ ] With secret configured: request to any `/api/v1/*` route with wrong / missing / empty `X-Gateway-Secret` returns HTTP `401`
- [ ] With secret configured: that `401` response body equals exactly `secret in header 'X-Gateway-Secret' is invalid => access denied`

**Auth — correct headers:**
- [ ] With secret configured: request to `/api/v1/*` with both correct headers reaches the inner handler and returns the same status it would return without auth
- [ ] With secret configured: the inner handler observes `req.Header.Get("X-Gateway-Secret") == ""` (header stripped before forwarding)

**Probes always open:**
- [ ] `/healthz`, `/readiness`, `/metrics` return their normal probe responses with no headers, with or without secret configured

**Empty-secret backward compatibility:**
- [ ] When secret is unset/empty: no auth middleware is mounted (any header passes through); a request to `/api/v1/*` succeeds with no auth headers
- [ ] When secret is unset/empty: exactly one `slog.WarnContext` line is emitted at startup containing the substring `gateway-secret not set`
- [ ] That same warning line also contains the substring `git-rest API is unauthenticated`

**Metrics integration:**
- [ ] Existing HTTP metrics observe `401` and `500` auth-failure responses (auth wraps inside the metrics middleware)

**Tests:**
- [ ] An integration / handler-level test wires the real `gorillamux.NewRouter()` from `createHTTPServer` (or its extracted equivalent), enables the auth middleware, and exercises: (a) missing initiator → 500 + body, (b) wrong secret → 401 + body, (c) correct headers → inner status + header stripped, (d) probe routes succeed with no headers
- [ ] `make precommit` passes

**End-to-end scenarios (integration seam — new HTTP auth contract):**
- [ ] `scenarios/008-gateway-secret-auth.md` (auth-enabled path: probes open, missing/wrong/correct headers) is `active` and passes against a freshly built binary
- [ ] `scenarios/009-gateway-secret-disabled.md` (empty-secret backward-compat + startup warning) is `active` and passes against a freshly built binary

## Verification

```bash
make precommit
```

Manual e2e (run after deploy with a known `$GATEWAY_SECRET`; substitute the actual host:port — `:8080` standalone, `:9090` in the K8s manifests):

```bash
HOST=<host>:9090

# probes always work, no headers
curl -fsS http://$HOST/healthz
curl -fsS http://$HOST/readiness
curl -fsS http://$HOST/metrics | head -3

# missing initiator → 500
curl -i http://$HOST/api/v1/files/README.md

# wrong secret → 401
curl -i \
  -H "X-Gateway-Initator: manual-test" \
  -H "X-Gateway-Secret: wrong" \
  http://$HOST/api/v1/files/README.md

# correct headers → 200
curl -fsS \
  -H "X-Gateway-Initator: manual-test" \
  -H "X-Gateway-Secret: $GATEWAY_SECRET" \
  http://$HOST/api/v1/files/README.md
```
