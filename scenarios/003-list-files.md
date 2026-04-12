---
status: draft
---

# Scenario 003: List files — glob pattern matching

Validates that git-rest lists files matching glob patterns.

## Setup

```bash
WORK_DIR=$(mktemp -d)
cd "$WORK_DIR" && git init && git commit --allow-empty -m "init"
```

```bash
cd ~/Documents/workspaces/git-rest && go run main.go --repo "$WORK_DIR" --listen :9090 --pull-interval 60s -logtostderr &
SERVER_PID=$!
sleep 2
```

- [ ] Create test files:
  ```bash
  curl -s -X POST http://localhost:9090/api/v1/files/notes/a.md -d 'note a'
  curl -s -X POST http://localhost:9090/api/v1/files/notes/b.md -d 'note b'
  curl -s -X POST http://localhost:9090/api/v1/files/notes/c.txt -d 'note c'
  curl -s -X POST http://localhost:9090/api/v1/files/other/d.md -d 'other d'
  ```

## Action

### List all markdown files
- [ ] `curl -s 'http://localhost:9090/api/v1/files/?glob=**/*.md'` returns 3 files (a.md, b.md, d.md)

### List files in subdirectory
- [ ] `curl -s 'http://localhost:9090/api/v1/files/?glob=notes/*.md'` returns 2 files (a.md, b.md)

### List with no matches
- [ ] `curl -s 'http://localhost:9090/api/v1/files/?glob=missing/*.md'` returns empty list

## Expected

- [ ] Glob patterns filter files correctly
- [ ] Response contains file paths relative to repo root
- [ ] No errors on empty result

## Cleanup

```bash
kill $SERVER_PID 2>/dev/null
rm -rf "$WORK_DIR"
```
