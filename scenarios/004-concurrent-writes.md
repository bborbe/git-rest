---
status: draft
---

# Scenario 004: Concurrent writes — no data loss under parallel requests

Validates that git-rest handles concurrent write requests without data loss or git conflicts.

## Setup

```bash
REMOTE_DIR=$(mktemp -d)
git init -q --bare "$REMOTE_DIR"
WORK_DIR=$(mktemp -d)
cd "$WORK_DIR" && git init -q && git remote add origin "$REMOTE_DIR" && git commit -q --allow-empty -m "init" && git push -q -u origin master
PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('', 0)); print(s.getsockname()[1]); s.close()")
BASE=http://localhost:$PORT
```

```bash
cd ~/Documents/workspaces/git-rest && go run main.go --repo "$WORK_DIR" --listen :$PORT --pull-interval 60s -logtostderr &
SERVER_PID=$!
sleep 2
```

## Action

### Write 10 files concurrently
- [ ] Run in parallel:
  ```bash
  for i in $(seq 1 10); do
    curl -s -X POST $BASE/api/v1/files/file-$i.md -d "content $i" &
  done
  wait
  ```

### Verify all files exist
- [ ] `ls "$WORK_DIR"/file-*.md | wc -l` returns 10
- [ ] Each file contains correct content
- [ ] `cd "$WORK_DIR" && git log --oneline | wc -l` shows 11 commits (init + 10 writes)
- [ ] `cd "$WORK_DIR" && git status` shows clean working tree

## Expected

- [ ] All 10 files created successfully
- [ ] No git lock errors
- [ ] All commits present in history
- [ ] No data corruption

## Cleanup

```bash
kill $SERVER_PID 2>/dev/null
rm -rf "$WORK_DIR" "$REMOTE_DIR"
```
