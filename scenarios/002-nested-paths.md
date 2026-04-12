---
status: draft
---

# Scenario 002: Nested paths — create and read files in subdirectories

Validates that git-rest handles nested directory paths correctly, creating parent dirs as needed.

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

- [ ] `curl -s http://localhost:9090/healthz` returns `OK`

## Action

### Create file in nested path
- [ ] `curl -s -X POST http://localhost:9090/api/v1/files/30%20Analysis/dev/2026-04-12-test.md -d '# Analysis'` returns 200
- [ ] `cat "$WORK_DIR/30 Analysis/dev/2026-04-12-test.md"` shows `# Analysis`
- [ ] Parent directories created automatically

### Read file from nested path
- [ ] `curl -s http://localhost:9090/api/v1/files/30%20Analysis/dev/2026-04-12-test.md` returns `# Analysis`

### Delete file from nested path
- [ ] `curl -s -X DELETE http://localhost:9090/api/v1/files/30%20Analysis/dev/2026-04-12-test.md` returns 200
- [ ] File no longer exists

## Expected

- [ ] Spaces in paths handled via URL encoding
- [ ] Nested directories created automatically on write
- [ ] Git commits include full relative path

## Cleanup

```bash
kill $SERVER_PID 2>/dev/null
rm -rf "$WORK_DIR"
```
