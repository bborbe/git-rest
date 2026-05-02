---
status: active
---

# Scenario 007: Path traversal and security boundaries

Validates that git-rest rejects path traversal attempts, absolute paths, and .git directory access. Encoded traversal and absolute paths reach the file handler and return `400` (handler-level rejection). Unencoded `../` paths are normalised by Go's `net/http` BEFORE routing, so they resolve outside the `/api/v1/files/` prefix and return `404` (no route match) — both 400 and 404 mean "request rejected, no file access," and either is acceptable for unencoded traversal cases.

## Setup

```bash
WORK_DIR=$(mktemp -d)/repo
PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('', 0)); print(s.getsockname()[1]); s.close()")
BASE=http://localhost:$PORT
```

```bash
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR" \
  --listen :$PORT \
  --pull-interval 60s \
  -logtostderr &
SERVER_PID=$!
sleep 3
```

- [ ] `curl -s $BASE/healthz` returns `OK`

### Seed a file for read/delete tests

- [ ] `curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/v1/files/docs/legit.md -d 'safe'` returns `200`

## Action

### Path traversal via ../  (unencoded — Go normalises before routing → 400 or 404)

- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files/../../../etc/passwd` returns `400` or `404`
- [ ] `curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/v1/files/../../../tmp/evil.md -d 'pwned'` returns `400` or `404`
- [ ] `curl -s -o /dev/null -w '%{http_code}' -X DELETE $BASE/api/v1/files/../../../etc/passwd` returns `400` or `404`

### Path traversal via encoded ../

- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files/..%2F..%2F..%2Fetc%2Fpasswd` returns `400`
- [ ] `curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/v1/files/docs%2F..%2F..%2Fevil.md -d 'pwned'` returns `400`

### Path traversal mid-path  (unencoded — Go normalises before routing → 400 or 404)

- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files/docs/../../../etc/passwd` returns `400` or `404`
- [ ] `curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/v1/files/a/b/../../../../../../tmp/evil.md -d 'pwned'` returns `400` or `404`

### .git directory access

- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files/.git/config` returns `400`
- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files/.git/HEAD` returns `400`
- [ ] `curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/v1/files/.git/hooks/pre-commit -d '#!/bin/sh'` returns `400`
- [ ] `curl -s -o /dev/null -w '%{http_code}' -X DELETE $BASE/api/v1/files/.git/config` returns `400`

### Absolute paths

- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files//etc/passwd` returns `400`

### Empty path

- [ ] `curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/v1/files/ -d 'empty'` returns `400` or `404`

### Legitimate paths still work

- [ ] `curl -s $BASE/api/v1/files/docs/legit.md` returns `safe`
- [ ] `curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/v1/files/normal/file.md -d 'ok'` returns `200`
- [ ] `curl -s $BASE/api/v1/files/normal/file.md` returns `ok`

### Filesystem not touched by rejected requests

- [ ] `test ! -f /tmp/evil.md` — traversal write did not escape repo
- [ ] `test ! -f "$WORK_DIR/../evil.md"` — no file outside repo dir

## Expected

- [ ] All `../` traversal attempts return 400 (not silently cleaned)
- [ ] `.git` directory access blocked on all HTTP methods
- [ ] Absolute paths rejected
- [ ] Legitimate nested paths continue to work
- [ ] No files created outside the repo directory

## Cleanup

```bash
kill $SERVER_PID 2>/dev/null
rm -rf "$WORK_DIR"
```
