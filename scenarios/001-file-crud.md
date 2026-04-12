---
status: draft
---

# Scenario 001: File CRUD — create, read, update, delete

Validates that git-rest creates, reads, updates, and deletes files with automatic git commits.

## Setup

```bash
WORK_DIR=$(mktemp -d)
cd "$WORK_DIR" && git init && git commit --allow-empty -m "init"
PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('', 0)); print(s.getsockname()[1]); s.close()")
BASE=http://localhost:$PORT
```

```bash
cd ~/Documents/workspaces/git-rest && go run main.go --repo "$WORK_DIR" --listen :$PORT --pull-interval 60s -logtostderr &
SERVER_PID=$!
sleep 2
```

- [ ] `curl -s $BASE/healthz` returns `OK`

## Action

### Create a file
- [ ] `curl -s -X POST $BASE/api/v1/files/test.md -d '# Hello'` returns 200
- [ ] `cat "$WORK_DIR/test.md"` shows `# Hello`
- [ ] `cd "$WORK_DIR" && git log --oneline -1` shows a commit for the file

### Read the file
- [ ] `curl -s $BASE/api/v1/files/test.md` returns `# Hello`

### Update the file
- [ ] `curl -s -X POST $BASE/api/v1/files/test.md -d '# Updated'` returns 200
- [ ] `curl -s $BASE/api/v1/files/test.md` returns `# Updated`
- [ ] `cd "$WORK_DIR" && git log --oneline | wc -l` shows 3 commits (init + create + update)

### Read non-existent file
- [ ] `curl -s -o /dev/null -w '%{http_code}' $BASE/api/v1/files/missing.md` returns `404`

### Delete the file
- [ ] `curl -s -X DELETE $BASE/api/v1/files/test.md` returns 200
- [ ] `test ! -f "$WORK_DIR/test.md"` — file no longer exists
- [ ] `cd "$WORK_DIR" && git log --oneline | wc -l` shows 4 commits

### Delete non-existent file
- [ ] `curl -s -o /dev/null -w '%{http_code}' -X DELETE $BASE/api/v1/files/missing.md` returns `404`

## Expected

- [ ] All CRUD operations return correct HTTP status codes
- [ ] Each write/delete produces a git commit
- [ ] File content matches what was sent

## Cleanup

```bash
kill $SERVER_PID 2>/dev/null
rm -rf "$WORK_DIR"
```
