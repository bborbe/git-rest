# git-rest

Lightweight REST API server for reading and writing files in a git repository. Automatic git commit and push on writes. Periodic git pull to stay in sync.

## Features

- `GET /api/v1/files/*path` — read file
- `POST /api/v1/files/*path` — create/update file + git commit + push
- `DELETE /api/v1/files/*path` — delete file + git commit + push
- Periodic git pull (configurable interval)
- No database, no web UI — just a REST API for a git repo

## Quick Start

```bash
git-rest \
  --repo /path/to/repo \
  --listen :8080 \
  --pull-interval 30s
```

## License

BSD-2-Clause
