---
status: active
---

# Scenario 008: Gateway-Secret HTTP auth — wrong / missing / correct headers

Validates that when `--gateway-secret` is configured, the `/api/v1/*` routes require both `X-Gateway-Initator` and matching `X-Gateway-Secret`, while `/healthz`, `/readiness`, and `/metrics` remain unauthenticated.

## Setup

```bash
REMOTE_DIR=$(mktemp -d)
git init -q --bare "$REMOTE_DIR"
WORK_DIR=$(mktemp -d)
cd "$WORK_DIR" && git init -q && git remote add origin "$REMOTE_DIR" && git commit -q --allow-empty -m "init" && git push -q -u origin master
PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('', 0)); print(s.getsockname()[1]); s.close()")
BASE=http://localhost:$PORT
SECRET=test-secret-$(date +%s)
```

```bash
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR" \
  --listen :$PORT \
  --pull-interval 60s \
  --gateway-secret "$SECRET" \
  -logtostderr &
SERVER_PID=$!
sleep 2
```

- [ ] `curl -s $BASE/healthz` returns `OK`

## Action

### Probes always reachable without headers
- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/healthz` returns `200`
- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/readiness` returns `200`
- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/metrics` returns `200`

### API rejects requests without `X-Gateway-Initator`
- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files/foo.md` returns `500`
- [ ] `curl -s $BASE/api/v1/files/foo.md` body equals `header 'X-Gateway-Initator' missing`

### API rejects requests with wrong `X-Gateway-Secret`
- [ ] `curl -s -o /dev/null -w '%{http_code}' -H "X-Gateway-Initator: scenario-008" -H "X-Gateway-Secret: wrong" $BASE/api/v1/files/foo.md` returns `401`
- [ ] `curl -s -H "X-Gateway-Initator: scenario-008" -H "X-Gateway-Secret: wrong" $BASE/api/v1/files/foo.md` body equals `secret in header 'X-Gateway-Secret' is invalid => access denied`

### API rejects requests with missing `X-Gateway-Secret` (initiator present)
- [ ] `curl -s -o /dev/null -w '%{http_code}' -H "X-Gateway-Initator: scenario-008" $BASE/api/v1/files/foo.md` returns `401`

### API accepts requests with both correct headers
- [ ] `curl -s -X POST -H "X-Gateway-Initator: scenario-008" -H "X-Gateway-Secret: $SECRET" -d '# Hello' $BASE/api/v1/files/test.md` returns 200
- [ ] `curl -s -H "X-Gateway-Initator: scenario-008" -H "X-Gateway-Secret: $SECRET" $BASE/api/v1/files/test.md` returns `# Hello`
- [ ] `cat "$WORK_DIR/test.md"` shows `# Hello`

## Expected

- [ ] Probes never require headers
- [ ] `/api/v1/*` rejects on missing initiator (`500`) and on bad/missing secret (`401`) with exact response bodies
- [ ] `/api/v1/*` succeeds with both correct headers
- [ ] Auth failures produce the exact body strings the spec specifies (no leading/trailing whitespace)

## Cleanup

```bash
kill $SERVER_PID 2>/dev/null
rm -rf "$WORK_DIR" "$REMOTE_DIR"
```

## Companion scenario (empty-secret backward compatibility)

A second scenario `009-gateway-secret-disabled.md` should cover the unset-secret path: no auth required, single startup `slog.WarnContext` line containing both `gateway-secret not set` and `git-rest API is unauthenticated`. Tracked separately because it asserts a startup log line, not just HTTP behavior.
