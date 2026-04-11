---
status: approved
spec: [001-git-rest-server]
created: "2026-04-11T21:40:00Z"
queued: "2026-04-11T19:38:44Z"
branch: dark-factory/git-rest-server
---

<summary>
- Multi-stage Dockerfile: Go build stage compiles static binary, Alpine runtime stage includes git and SSH client
- Runtime image has git, openssh-client, ca-certificates, gnupg, and tzdata for git operations over SSH/HTTPS
- Binary is statically linked (CGO_ENABLED=0) and stripped (-s flag)
- Build args for git commit hash and build date are baked into the image as environment variables
- Entrypoint is the compiled binary — no shell wrapper
</summary>

<objective>
Add a production Dockerfile that builds the git-rest binary and packages it in a minimal Alpine image with the git toolchain required for runtime git operations (add, commit, push, pull). The image must be deployable as a K8s StatefulSet.
</objective>

<context>
Read `CLAUDE.md` and `docs/dod.md` for project conventions.

Reference Dockerfile pattern (from agent/task/controller):
```dockerfile
ARG DOCKER_REGISTRY=docker.quant.benjamin-borbe.de:443
FROM ${DOCKER_REGISTRY}/golang:1.26.2 AS build
ARG BUILD_GIT_COMMIT=none
ARG BUILD_DATE=unknown
COPY . /workspace
WORKDIR /workspace
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -mod=vendor -ldflags "-s" -a -installsuffix cgo -o /main
CMD ["/bin/bash"]

FROM ${DOCKER_REGISTRY}/alpine:3.23
RUN apk --no-cache add \
    ca-certificates \
    git \
    gnupg \
    openssh-client \
    tzdata \
    && rm -rf /var/cache/apk/*
ARG BUILD_GIT_COMMIT=none
ARG BUILD_DATE=unknown
COPY --from=build /main /main
ENV BUILD_GIT_COMMIT=${BUILD_GIT_COMMIT}
ENV BUILD_DATE=${BUILD_DATE}
ENTRYPOINT ["/main"]
```

Existing files:
- `main.go` — server entry point (created by prompt 3)
- `go.mod` — module `github.com/bborbe/git-rest`, Go 1.26.2
- `vendor/` — vendored dependencies
</context>

<requirements>
1. Create `Dockerfile` at the project root following the reference pattern exactly:
   - Build stage: `${DOCKER_REGISTRY}/golang:1.26.2`, copy source, build static binary with `-trimpath -mod=vendor -ldflags "-s" -a -installsuffix cgo`
   - Runtime stage: `${DOCKER_REGISTRY}/alpine:3.23` with `ca-certificates`, `git`, `gnupg`, `openssh-client`, `tzdata`
   - Copy binary from build stage, set `ENTRYPOINT ["/main"]`
   - Include `BUILD_GIT_COMMIT` and `BUILD_DATE` build args as env vars

2. No changes to existing code — this prompt only adds the Dockerfile
</requirements>

<constraints>
- Use the private registry `docker.quant.benjamin-borbe.de:443` as default `DOCKER_REGISTRY`
- Go version must match `go.mod` (1.26.2)
- Alpine version 3.23
- Runtime must include `git` — the server shells out to the git binary at runtime
- Binary must be statically linked (`CGO_ENABLED=0`)
- Do NOT commit — dark-factory handles git
</constraints>

<verification>
```bash
# Confirm Dockerfile exists and has correct structure
grep -n "golang:1.26.2\|alpine:3.23\|git\|ENTRYPOINT" /workspace/Dockerfile

# Confirm build compiles (dry run)
cd /workspace && go build -trimpath -mod=vendor -ldflags "-s" -o /tmp/git-rest .
```
</verification>
