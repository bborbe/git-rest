---
status: active
---

# Scenario 009: Gateway-Secret unset — auth disabled, startup warning

Validates that when `--gateway-secret` is empty/unset, the `/api/v1/*` routes accept requests without any auth headers (backward-compatible mode), and exactly one structured warning line is emitted at startup.

## Setup

```bash
REMOTE_DIR=$(mktemp -d)
git init -q --bare "$REMOTE_DIR"
WORK_DIR=$(mktemp -d)
cd "$WORK_DIR" && git init -q && git remote add origin "$REMOTE_DIR" && git commit -q --allow-empty -m "init" && git push -q -u origin master
PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('', 0)); print(s.getsockname()[1]); s.close()")
BASE=http://localhost:$PORT
LOG=$(mktemp)
```

```bash
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR" \
  --listen :$PORT \
  --pull-interval 60s \
  -logtostderr 2>"$LOG" &
SERVER_PID=$!
sleep 2
```

- [ ] `curl -s $BASE/healthz` returns `OK`

## Action

### API accepts requests without any auth headers
- [ ] `curl -s -X POST -d '# Hello' $BASE/api/v1/files/test.md` returns 200
- [ ] `curl -s $BASE/api/v1/files/test.md` returns `# Hello`

### Probes still work
- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/healthz` returns `200`
- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/readiness` returns `200`

### Exactly one startup warning line
- [ ] `grep -c 'gateway-secret not set' "$LOG"` outputs `1`
- [ ] `grep 'gateway-secret not set' "$LOG"` line also contains `git-rest API is unauthenticated`

## Expected

- [ ] No auth required when `--gateway-secret` is absent
- [ ] Exactly one warning line at startup containing both substrings
- [ ] No regression vs. pre-spec behavior — existing deployments with no `GATEWAY_SECRET` env keep working

## Cleanup

```bash
kill $SERVER_PID 2>/dev/null
rm -rf "$WORK_DIR" "$REMOTE_DIR" "$LOG"
```
