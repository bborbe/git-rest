---
status: active
---

# Scenario 006: Local repo without remote

Validates that git-rest initializes a local repository, handles file operations without push, and skips pull gracefully when no remote is configured.

## Setup

```bash
PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('', 0)); print(s.getsockname()[1]); s.close()")
BASE=http://localhost:$PORT
WORK_DIR=$(mktemp -d)/repo
# Note: WORK_DIR does not exist yet — git-rest should create it
```

## Action

### Start with non-existent repo path (auto-init)

```bash
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR" \
  --listen :$PORT \
  --pull-interval 5s \
  -logtostderr &
SERVER_PID=$!
sleep 3
```

- [ ] Server started without error
- [ ] `test -d "$WORK_DIR/.git"` — repo was initialized
- [ ] `curl -s $BASE/healthz` returns `OK`

### Readiness returns 200 for empty local repo

- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/readiness` returns `200`

### Create a file (commit without push)

- [ ] `curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/v1/files/hello.md -d 'hello world'` returns `200`
- [ ] `curl -s $BASE/api/v1/files/hello.md` returns `hello world`
- [ ] `git -C "$WORK_DIR" log --oneline -1` shows commit message containing `hello.md`

### Update the file

- [ ] `curl -s -o /dev/null -w '%{http_code}' -X POST $BASE/api/v1/files/hello.md -d 'updated'` returns `200`
- [ ] `curl -s $BASE/api/v1/files/hello.md` returns `updated`
- [ ] `git -C "$WORK_DIR" log --oneline | wc -l` shows 2 commits

### Delete the file

- [ ] `curl -s -o /dev/null -w '%{http_code}' -X DELETE $BASE/api/v1/files/hello.md` returns `200`
- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files/hello.md` returns `404`
- [ ] `git -C "$WORK_DIR" log --oneline | wc -l` shows 3 commits

### Readiness after operations

- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/readiness` returns `200`

### Pull interval runs without error

- [ ] Wait 6 seconds (pull interval is 5s), check server logs — no pull errors
- [ ] `curl -s $BASE/healthz` returns `OK` (server still running)

### Verify no remote configured

- [ ] `git -C "$WORK_DIR" remote` returns empty output
- [ ] `git -C "$WORK_DIR" log --oneline | wc -l` shows 3 (all commits local)

### Restart with existing local repo

```bash
kill $SERVER_PID 2>/dev/null
sleep 1
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR" \
  --listen :$PORT \
  --pull-interval 60s \
  -logtostderr &
SERVER_PID=$!
sleep 2
```

- [ ] Server started without error (init skipped, repo exists)
- [ ] `curl -s $BASE/healthz` returns `OK`
- [ ] `git -C "$WORK_DIR" log --oneline | wc -l` still shows 3 commits

### Repo path is a file (not directory)

```bash
kill $SERVER_PID 2>/dev/null
sleep 1
FILE_PATH=$(mktemp)
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$FILE_PATH" \
  --listen :$PORT \
  -logtostderr 2>&1 | head -5 &
FAIL_PID=$!
sleep 2
```

- [ ] Process exited with error
- [ ] Error message mentions "not a directory" or similar

### Parent directories created automatically

```bash
kill $FAIL_PID 2>/dev/null
sleep 1
DEEP_DIR=$(mktemp -d)/a/b/c/repo
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$DEEP_DIR" \
  --listen :$PORT \
  -logtostderr &
DEEP_PID=$!
sleep 3
```

- [ ] Server started without error
- [ ] `test -d "$DEEP_DIR/.git"` — nested repo was initialized
- [ ] `curl -s $BASE/healthz` returns `OK`

## Expected

- [ ] Auto-init creates the repo directory and runs git init
- [ ] File CRUD works without a remote (commit succeeds, push skipped)
- [ ] Pull interval does not produce errors
- [ ] Readiness returns 200 for clean local-only repo
- [ ] Restart with existing repo skips init
- [ ] Repo path pointing to a file fails with clear error
- [ ] Deeply nested repo paths are created automatically

## Cleanup

```bash
kill $SERVER_PID 2>/dev/null
kill $FAIL_PID 2>/dev/null
kill $DEEP_PID 2>/dev/null
rm -rf "$WORK_DIR" "$FILE_PATH" "$DEEP_DIR"
```
