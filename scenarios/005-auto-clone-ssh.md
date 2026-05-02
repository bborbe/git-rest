---
status: active
---

# Scenario 005: Auto-clone from a remote repository on startup

Validates that git-rest clones a remote repository on startup (creating parent dirs as needed), configures the configured git identity in the clone, runs subsequent operations against the cloned working tree, and skips clone on restart when the repo already exists.

The clone path is verified using a public HTTPS remote (`https://github.com/bborbe/run.git`) so the scenario runs locally without SSH credentials. Production uses SSH (the StatefulSet manifests pass `--git-ssh-key`); the SSH-specific transport is exercised in production by the live `vault-obsidian-*` pods. This scenario covers the bootstrap logic that is identical for both transports: clone-if-empty, skip-if-exists, parent-dir-create, fail-on-bad-key.

## Setup

```bash
WORK_DIR=$(mktemp -d)/repo                # does not exist yet — git-rest must create it
PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('', 0)); print(s.getsockname()[1]); s.close()")
BASE=http://localhost:$PORT
REMOTE_URL="https://github.com/bborbe/run.git"
```

## Action

### Start with auto-clone (HTTPS public remote, no SSH key needed)

```bash
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR" \
  --listen :$PORT \
  --pull-interval 60s \
  --git-remote-url "$REMOTE_URL" \
  --git-user-name "git-rest-bot" \
  --git-user-email "bot@git-rest" \
  -logtostderr &
SERVER_PID=$!
# Allow time for clone over the network
for i in $(seq 1 30); do curl -fsS "$BASE/healthz" >/dev/null 2>&1 && break; sleep 1; done
```

- [ ] Server started without error
- [ ] `test -d "$WORK_DIR/.git"` — repo was cloned
- [ ] `curl -s $BASE/healthz` returns `OK`

### Verify clone content matches the public repo

- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files/README.md` returns `200` (file exists in `bborbe/run`)
- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files/LICENSE` returns `200`
- [ ] `git -C "$WORK_DIR" log --oneline | wc -l` is `>= 1` (clone has commits)
- [ ] `git -C "$WORK_DIR" remote get-url origin` returns the configured remote URL

### Verify git user identity is configured in the clone

- [ ] `git -C "$WORK_DIR" config user.name` returns `git-rest-bot`
- [ ] `git -C "$WORK_DIR" config user.email` returns `bot@git-rest`

### Restart with existing repo (clone skipped)

```bash
kill $SERVER_PID 2>/dev/null
sleep 1
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR" \
  --listen :$PORT \
  --pull-interval 60s \
  --git-remote-url "$REMOTE_URL" \
  --git-user-name "git-rest-bot" \
  --git-user-email "bot@git-rest" \
  -logtostderr &
SERVER_PID=$!
for i in $(seq 1 20); do curl -fsS "$BASE/healthz" >/dev/null 2>&1 && break; sleep 0.5; done
```

- [ ] Server started without error (clone path skipped because `$WORK_DIR/.git` already exists)
- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files/README.md` still returns `200`

### Start without remote-url (backward compatible — local-only mode)

```bash
kill $SERVER_PID 2>/dev/null
sleep 1
WORK_DIR2=$(mktemp -d)/local-only
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR2" \
  --listen :$PORT \
  --pull-interval 60s \
  -logtostderr &
SERVER_PID=$!
for i in $(seq 1 20); do curl -fsS "$BASE/healthz" >/dev/null 2>&1 && break; sleep 0.5; done
```

- [ ] Server started without error (no remote, init-only)
- [ ] `test -d "$WORK_DIR2/.git"` — local repo initialised
- [ ] `git -C "$WORK_DIR2" remote` returns empty (no origin)

### Missing SSH key file fails fast (when --git-ssh-key is set but file missing)

```bash
kill $SERVER_PID 2>/dev/null
sleep 1
WORK_DIR3=$(mktemp -d)/badkey
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR3" \
  --listen :$PORT \
  --git-remote-url "$REMOTE_URL" \
  --git-ssh-key /nonexistent/key \
  -logtostderr >/tmp/srv-005-badkey.log 2>&1 &
FAIL_PID=$!
sleep 3
```

- [ ] Process is no longer running (exited on bad key) OR clone fails — verify with `pgrep` or healthz unreachable
- [ ] `/tmp/srv-005-badkey.log` mentions the missing key path or an SSH-related error

## Expected

- [ ] Auto-clone creates the repo directory (including parent dirs) and clones from the remote
- [ ] Git user identity is configured in the cloned repo
- [ ] Configured remote URL is preserved as `origin`
- [ ] Restart with existing repo skips the clone path (idempotent bootstrap)
- [ ] No `--git-remote-url` flag = local-only init (backward compatible)
- [ ] Missing SSH key file does not silently succeed when SSH transport is requested

## Cleanup

```bash
kill $SERVER_PID 2>/dev/null
kill $FAIL_PID 2>/dev/null
pkill -f "main.go --repo $WORK_DIR" 2>/dev/null
rm -rf "$(dirname $WORK_DIR)" "$(dirname $WORK_DIR2)" "$(dirname $WORK_DIR3)"
```

## Notes

- Internet access required (HTTPS clone from github.com).
- Production deployments use SSH transport; SSH-specific behavior (key file resolution, `GIT_SSH_COMMAND` derivation) is covered by the running `vault-obsidian-*` pods in dev/prod. A scenario that exercises SSH auth locally would need either a localhost SSH daemon or a throwaway deploy-key — out of scope here.
