---
status: completed
spec: [001-git-rest-server]
summary: Created production Dockerfile with golang:1.26.2 build stage and alpine:3.23 runtime, added REGISTRY/IMAGE/BRANCH variables and build/upload/clean/buca targets to Makefile, removed Makefile.docker.
container: git-rest-005-dockerfile
dark-factory-version: v0.108.0-dirty
created: "2026-04-11T21:40:00Z"
queued: "2026-04-11T20:12:01Z"
started: "2026-04-11T20:12:03Z"
completed: "2026-04-11T20:15:55Z"
branch: dark-factory/git-rest-server
---

<summary>
- Production Docker image builds the git-rest server as a single static binary
- Runtime image includes the git toolchain needed for git add/commit/push/pull operations
- Image supports SSH and HTTPS git remotes out of the box
- Build metadata (git commit, build date) is embedded in the image for traceability
- Makefile gets docker build/upload/clean/buca targets for the standard deployment workflow
</summary>

<objective>
Add a production Dockerfile and integrate docker build targets into the Makefile so that `make build`, `make upload`, `make clean`, and `make buca` work for building and pushing the git-rest Docker image.
</objective>

<context>
Read `CLAUDE.md` and `docs/dod.md` for project conventions.

Reference project (backup/service) — standalone repo with docker in one Makefile:
```makefile
REGISTRY ?= docker.io
IMAGE ?= bborbe/backup
BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)

build:
	go mod vendor
	docker build --no-cache --rm=true --platform=linux/amd64 -t $(REGISTRY)/$(IMAGE):$(BRANCH) -f Dockerfile .

upload:
	docker push $(REGISTRY)/$(IMAGE):$(BRANCH)

clean:
	docker rmi $(REGISTRY)/$(IMAGE):$(BRANCH) || true
	rm -rf vendor
```

Reference Dockerfile (from agent/task/controller — needs git at runtime):
```dockerfile
ARG DOCKER_REGISTRY=docker.io
FROM golang:1.26.2 AS build
COPY . /workspace
WORKDIR /workspace
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -mod=vendor -ldflags "-s" -a -installsuffix cgo -o /main
CMD ["/bin/bash"]

FROM alpine:3.23
RUN apk --no-cache add \
    ca-certificates \
    git \
    gnupg \
    openssh-client \
    tzdata \
    && rm -rf /var/cache/apk/*
COPY --from=build /main /main
ENTRYPOINT ["/main"]
```

Existing files:
- `main.go` — server entry point (created by prompt 3)
- `go.mod` — module `github.com/bborbe/git-rest`, Go 1.26.2
- `Makefile` — has precommit targets, needs docker targets added
- `Makefile.docker` — exists but should be REMOVED (standalone repo pattern puts everything in one Makefile)
</context>

<requirements>
1. Create `Dockerfile` at the project root:
   - Build stage: `golang:1.26.2` (public docker.io), copy source, build static binary with `-trimpath -mod=vendor -ldflags "-s" -a -installsuffix cgo`
   - Runtime stage: `alpine:3.23` with `ca-certificates`, `git`, `gnupg`, `openssh-client`, `tzdata`
   - Copy binary from build stage, set `ENTRYPOINT ["/main"]`
   - Use public images (docker.io), no private registry ARG needed

2. Add docker variables to the top of `Makefile` (after existing `VERSION` and `LDFLAGS` lines):
   ```makefile
   REGISTRY ?= docker.io
   IMAGE ?= bborbe/git-rest
   BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)
   ```

3. Add docker targets to `Makefile`:
   ```makefile
   .PHONY: build
   build:
   	go mod vendor
   	docker build --no-cache --rm=true --platform=linux/amd64 -t $(REGISTRY)/$(IMAGE):$(BRANCH) -f Dockerfile .

   .PHONY: upload
   upload:
   	docker push $(REGISTRY)/$(IMAGE):$(BRANCH)

   .PHONY: clean
   clean:
   	docker rmi $(REGISTRY)/$(IMAGE):$(BRANCH) || true
   	rm -rf vendor

   .PHONY: buca
   buca: build upload clean
   ```
   Note: replace the existing `build` target (which does `go build -o bin/git-rest`) — docker build replaces it.

4. Delete `Makefile.docker` — standalone repo does not use separate include files
</requirements>

<constraints>
- Use public docker.io images — git-rest is not a private project
- Go version must match `go.mod` (1.26.2)
- Alpine version 3.23
- Runtime must include `git` — the server shells out to the git binary at runtime
- Binary must be statically linked (`CGO_ENABLED=0`)
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
</constraints>

<verification>
```bash
make precommit
```

Additional checks:
```bash
# Confirm Dockerfile exists and has correct structure
grep -n "golang:1.26.2\|alpine:3.23\|git\|ENTRYPOINT" /workspace/Dockerfile

# Confirm Makefile has docker targets
grep -n "REGISTRY\|IMAGE\|BRANCH\|docker build\|docker push" /workspace/Makefile

# Confirm Makefile.docker is gone
test ! -f /workspace/Makefile.docker && echo "OK" || echo "FAIL: Makefile.docker still exists"
```
</verification>
