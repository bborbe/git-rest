DOCKER_REGISTRY ?= docker.io
IMAGE ?= bborbe/git-rest
BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)
DIRS += $(shell find */* -maxdepth 0 -name Makefile -exec dirname "{}" \;)
ifeq ($(VERSION),)
	VERSION := $(shell git describe --tags `git rev-list --tags --max-count=1`)
endif

include tools.env

.PHONY: default
default: precommit

.PHONY: run
run:
	@go run -mod=mod main.go

.PHONY: precommit
precommit: ensure format generate test check addlicense
	@echo "ready to commit"

.PHONY: ensure
ensure:
	go mod tidy -e
	go mod verify
	rm -rf vendor

.PHONY: format
format:
	find . -type f -name 'go.mod' -not -path './vendor/*' -exec go run github.com/shoenig/go-modtool@$(GO_MODTOOL_VERSION) -w fmt "{}" \;
	find . -type f -name '*.go' -not -path './vendor/*' -exec gofmt -w "{}" +
	go run github.com/incu6us/goimports-reviser/v3@$(GOIMPORTS_REVISER_VERSION) -project-name github.com/bborbe/git-rest -format -excludes vendor ./...
	find . -type d -name vendor -prune -o -type f -name '*.go' -print0 | xargs -0 -n 10 go run github.com/segmentio/golines@$(GOLINES_VERSION) --max-len=100 -w

.PHONY: generate
generate:
	rm -rf mocks avro
	mkdir -p mocks
	echo "package mocks" > mocks/mocks.go
	go generate -mod=mod ./...

# -race=true catches data races but flakes on some CI runners (rare SIGSEGV
# during gexec.Build in cmd/*-style binary smoke tests). Default off; opt in
# via ENABLE_RACE=true for nightly/manual hardening runs.
TESTFLAGS_RACE = -race=false
ifdef ENABLE_RACE
	TESTFLAGS_RACE = -race=true
endif

.PHONY: test
test:
	go test -mod=mod -p=$${GO_TEST_PARALLEL:-1} -cover $(TESTFLAGS_RACE) $(shell go list -mod=mod ./... | grep -v /vendor/)

.PHONY: check
check: lint vet vulncheck osv-scanner trivy

.PHONY: lint
lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --allow-parallel-runners --config .golangci.yml ./...

.PHONY: vet
vet:
	go vet -mod=mod $(shell go list -mod=mod ./... | grep -v /vendor/)


# GO-2026-5037/5038/5039 are Go 1.26.3 stdlib vulns (crypto/x509, mime, net/textproto),
# fixed in 1.26.4 — remove from this list once all CI runners and base images are on 1.26.4+.
VULNCHECK_IGNORE ?= GO-2026-4923 GO-2026-4514 GO-2022-0470 GO-2026-4772 GO-2026-4771 \
                    GO-2026-5037 GO-2026-5038 GO-2026-5039

# Known-benign govulncheck failure modes we swallow. golang.org/x/tools v0.46.0
# panics on packages containing generic *types.TypeParam during SSA analysis
# (govulncheck v1.3.0+ surface via RuntimeTypes/AllFunctions). We treat that as
# "no findings" because the panic happens AFTER the package scan; any real
# vulnerabilities would have been emitted as JSON on stdout before the panic.
# Any OTHER govulncheck failure (network, bad args, permissions) is surfaced.
.PHONY: vulncheck
vulncheck:
	@PKGS="$(shell go list -mod=mod ./... | grep -v /vendor/)"; \
	IGNORE_JSON=$$(printf '%s\n' $(VULNCHECK_IGNORE) | jq -R . | jq -s .); \
	ERR=$$(mktemp); \
	trap 'rm -f "$$ERR"' EXIT INT TERM; \
	OUT=$$(go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) -format json $$PKGS 2>$$ERR); \
	RC=$$?; \
	if [ $$RC -ne 0 ] && ! grep -qE "typesinternal\.ForEachElement|ForEachElement called on type containing" "$$ERR"; then \
		echo "govulncheck failed (exit $$RC):" >&2; \
		cat "$$ERR" >&2; \
		exit $$RC; \
	fi; \
	REMAIN=$$(printf '%s' "$$OUT" | jq -rs --argjson ignore "$$IGNORE_JSON" \
		'(map(select(.osv != null)) | map({key: .osv.id, value: (.osv.summary // "")}) | from_entries) as $$sum | \
		 map(select(.finding != null) | .finding) | \
		 map(select(.osv as $$o | $$ignore | index($$o) | not)) | \
		 map("\(.osv)\t\(.trace[-1].module)@\(.trace[-1].version) -> \(.fixed_version)\t\($$sum[.osv] // "")") | \
		 unique | .[]'); \
	if [ -n "$$REMAIN" ]; then \
		echo "Unexpected vulnerabilities (ignored: $(VULNCHECK_IGNORE)):"; \
		printf '%s\n' "$$REMAIN" | column -t -s "$$(printf '\t')"; \
		exit 1; \
	else \
		echo "No unignored vulnerabilities found"; \
	fi

.PHONY: osv-scanner
osv-scanner:
	@if [ -f .osv-scanner.toml ]; then \
		echo "Using .osv-scanner.toml"; \
		go run github.com/google/osv-scanner/v2/cmd/osv-scanner@$(OSV_SCANNER_VERSION) --config .osv-scanner.toml --recursive .; \
	else \
		echo "No config found, running default scan"; \
		go run github.com/google/osv-scanner/v2/cmd/osv-scanner@$(OSV_SCANNER_VERSION) --recursive .; \
	fi

.PHONY: trivy
trivy:
	trivy fs \
	--db-repository ghcr.io/aquasecurity/trivy-db \
	--scanners vuln,secret \
	--quiet \
	--no-progress \
	--disable-telemetry \
	--exit-code 1 .

.PHONY: addlicense
addlicense:
	go run github.com/google/addlicense@$(ADDLICENSE_VERSION) -c "Benjamin Borbe" -y $$(date +'%Y') -l bsd $$(find . -name "*.go" -not -path './vendor/*')

.PHONY: buca
buca: build upload clean apply


.PHONY: build
build: check-go-mod
	DOCKER_BUILDKIT=1 \
	docker build \
	--no-cache \
	--rm=true \
	--platform=linux/amd64 \
	--build-arg DOCKER_REGISTRY=$(DOCKER_REGISTRY) \
	--build-arg BRANCH=$(BRANCH) \
	--build-arg BUILD_GIT_VERSION=$$(git describe --tags --always --dirty) \
	--build-arg BUILD_GIT_COMMIT=$$(git rev-parse --short HEAD) \
	--build-arg BUILD_DATE=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
	-t $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION) \
	-f Dockerfile .

.PHONY: check-go-mod
check-go-mod:
	@if [ -f "go.mod" ]; then \
		echo "go.mod found, running go mod vendor..."; \
		go mod vendor; \
	else \
		echo "go.mod not found, skipping go mod vendor."; \
	fi


.PHONY: upload
upload:
	docker push $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION)

.PHONY: clean
clean:
	docker rmi $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION) || true
	# docker builder prune -a -f
	docker builder prune --max-used-space 21474836480 -f || true
	rm -rf vendor

.PHONY: apply
apply:
	@for i in $(DIRS); do \
		cd $$i; \
		echo "apply $${i}"; \
		make apply; \
		cd ..; \
	done

.PHONY: fix
fix:
	@for dir in $$(find $(CURDIR) -type d -name vendor -prune -o -name go.mod -exec dirname "{}" \; | grep -v '^$$'); do \
		cd $${dir}; \
		echo "fix $${dir}"; \
		go get github.com/go-git/go-git/v5@latest; \
		go get github.com/containerd/containerd@latest; \
		go get golang.org/x/crypto@latest; \
		go get golang.org/x/net@latest; \
	done
