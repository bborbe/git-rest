---
status: draft
---

# Scenario 005: Auto-clone with SSH key and git user config

Validates that git-rest clones a remote repository on startup, configures SSH for git operations, and sets git user identity.

## Setup

```bash
# Create a bare remote repo with one file
REMOTE_DIR=$(mktemp -d)
git init -q --bare "$REMOTE_DIR"
SEED_DIR=$(mktemp -d)
cd "$SEED_DIR" && git init -q && git remote add origin "$REMOTE_DIR"
git -C "$SEED_DIR" config user.name "seed" && git -C "$SEED_DIR" config user.email "seed@test"
echo "# Readme" > "$SEED_DIR/README.md"
cd "$SEED_DIR" && git add . && git commit -q -m "seed" && git push -q -u origin master

# Empty repo dir for git-rest to clone into
WORK_DIR=$(mktemp -d)/repo
# Note: WORK_DIR does not exist yet — git-rest should create it

# Generate a throwaway SSH key (not actually used for auth since remote is local)
SSH_KEY=$(mktemp)
ssh-keygen -t ed25519 -f "$SSH_KEY" -N "" -q

PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('', 0)); print(s.getsockname()[1]); s.close()")
BASE=http://localhost:$PORT
```

## Action

### Start with auto-clone

```bash
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR" \
  --listen :$PORT \
  --pull-interval 60s \
  --git-remote-url "$REMOTE_DIR" \
  --git-ssh-key "$SSH_KEY" \
  --git-user-name "git-rest-bot" \
  --git-user-email "bot@git-rest" \
  -logtostderr &
SERVER_PID=$!
sleep 3
```

- [ ] Server started without error
- [ ] `test -d "$WORK_DIR/.git"` — repo was cloned
- [ ] `curl -s $BASE/healthz` returns `OK`

### Verify clone content

- [ ] `curl -s $BASE/api/v1/files/README.md` returns `# Readme`

### Verify git user config

- [ ] `git -C "$WORK_DIR" config user.name` returns `git-rest-bot`
- [ ] `git -C "$WORK_DIR" config user.email` returns `bot@git-rest`

### Verify commits use configured identity

- [ ] `curl -s -X POST $BASE/api/v1/files/test.md -d 'hello'` returns 200
- [ ] `git -C "$WORK_DIR" log -1 --format='%an <%ae>'` returns `git-rest-bot <bot@git-rest>`

### Verify SSH key is used for push

- [ ] `git -C "$WORK_DIR" log --oneline | wc -l` shows 2 commits (seed + create)
- [ ] `git -C "$REMOTE_DIR" log --oneline | wc -l` shows 2 commits (push succeeded)

### Restart with existing repo (skip clone)

```bash
kill $SERVER_PID 2>/dev/null
sleep 1
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR" \
  --listen :$PORT \
  --pull-interval 60s \
  --git-remote-url "$REMOTE_DIR" \
  --git-ssh-key "$SSH_KEY" \
  --git-user-name "git-rest-bot" \
  --git-user-email "bot@git-rest" \
  -logtostderr &
SERVER_PID=$!
sleep 2
```

- [ ] Server started without error (clone skipped)
- [ ] `curl -s $BASE/api/v1/files/test.md` returns `hello` (file from prior run)

### Start without new flags (backward compatible)

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

- [ ] Server started without error (no SSH, no clone, no user config)
- [ ] `curl -s $BASE/api/v1/files/test.md` returns `hello`

### Missing SSH key file fails fast

```bash
kill $SERVER_PID 2>/dev/null
sleep 1
cd ~/Documents/workspaces/git-rest && go run main.go \
  --repo "$WORK_DIR" \
  --listen :$PORT \
  --git-ssh-key /nonexistent/key \
  -logtostderr 2>&1 | head -5 &
FAIL_PID=$!
sleep 2
```

- [ ] Process exited with error
- [ ] Error message mentions the missing key path

## Expected

- [ ] Auto-clone creates the repo directory and clones from remote
- [ ] SSH key is used for all git operations (clone, push)
- [ ] Git user identity is set in the cloned repo
- [ ] Commits use the configured identity
- [ ] Restart with existing repo skips clone
- [ ] No new flags = identical to current behavior
- [ ] Missing SSH key file fails startup immediately

## Cleanup

```bash
kill $SERVER_PID 2>/dev/null
kill $FAIL_PID 2>/dev/null
rm -rf "$WORK_DIR" "$REMOTE_DIR" "$SEED_DIR" "$SSH_KEY" "${SSH_KEY}.pub"
```
