DOCKER_REGISTRY ?= docker.io
IMAGE ?= bborbe/git-rest
BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)
DIRS += $(shell find */* -maxdepth 0 -name Makefile -exec dirname "{}" \;)
ifeq ($(VERSION),)
	VERSION := $(shell git describe --tags `git rev-list --tags --max-count=1`)
endif

include tools.env

# Which machine builds the image. `local` builds here with the local daemon; any
# other value is a cluster alias (sun, nukedev, nukeprod) or a raw kubectl
# context name, and the build runs as a Job there.
# Unset BUILD_TARGET resolves automatically: a bounded `kubectl get --raw /readyz`
# probe through hack/cluster-alias.sh picks sun when it answers and the local
# daemon when it does not. An explicit BUILD_TARGET always wins and the probe is
# never run. SUN_PROBE=fail short-circuits the probe to "unreachable", so the
# fallback can be exercised without touching the cluster:
#   make -n build SUN_PROBE=fail
#
# The probe is cheap where sun is absent: with no sun kubeconfig on the host,
# kubectl fails immediately, so CI pays nothing. It costs the timeout only on a
# machine that holds the kubeconfig but cannot reach the cluster.
BUILD_TARGET_PROBE_TIMEOUT ?= 5
SUN_PROBE ?=

ifeq ($(origin BUILD_TARGET),undefined)
  ifeq ($(SUN_PROBE),fail)
    BUILD_TARGET := local
    BUILD_TARGET_REASON := sun unreachable: SUN_PROBE=fail
  else
    BUILD_TARGET_PROBE_OUT := $(shell BUILD_TARGET=sun bash -c 'source hack/cluster-alias.sh && kc --request-timeout=$(BUILD_TARGET_PROBE_TIMEOUT)s get --raw /readyz' 2>/dev/null | grep -qx ok && echo ok)
    ifeq ($(BUILD_TARGET_PROBE_OUT),ok)
      BUILD_TARGET := sun
      BUILD_TARGET_REASON := probe ok
      # The cluster arm needs a REF and a TAG. Default them so a bare `make build`
      # keeps working on a sun-reachable host: REF to HEAD, TAG to a slash-free
      # scratch tag derived from the branch and the commit. Never the bare branch
      # name -- `hack/build-cluster.sh` rejects a TAG containing `/`, and a cluster
      # build must never overwrite a moving tag such as `:master`.
      REF ?= $(shell git rev-parse HEAD)
      VERSION ?= $(subst /,-,$(BRANCH))-$(shell git rev-parse --short HEAD)
    else
      BUILD_TARGET := local
      BUILD_TARGET_PROBE_ERR := $(shell BUILD_TARGET=sun bash -c 'source hack/cluster-alias.sh && kc --request-timeout=$(BUILD_TARGET_PROBE_TIMEOUT)s get --raw /readyz' 2>&1 >/dev/null | head -1)
      BUILD_TARGET_REASON := sun unreachable: $(if $(BUILD_TARGET_PROBE_ERR),$(BUILD_TARGET_PROBE_ERR),probe failed)
    endif
  endif
  $(info build target: $(BUILD_TARGET) ($(BUILD_TARGET_REASON)))
else
  $(info build target: $(BUILD_TARGET) (explicit))
endif

# Cluster-build inputs. The vendored cluster script defaults the namespace,
# timeout and Dockerfile; the clone URL, tag and image come from this repository.
BUILD_TIMEOUT ?= 3600
BUILD_REPO_URL ?= https://github.com/$(IMAGE).git
BUILD_DOCKERFILE ?= Dockerfile
BUILD_PUSH_SECRET ?= buildkit-push

# The commit the cluster clones. Empty on purpose: the cluster arm must be told
# which commit to build, and the script refuses to run without it.
REF ?=

# Package + publish the Helm chart in helm/ to OCI. Chart version comes from
# helm/Chart.yaml (independent of the binary VERSION). Requires a prior
# `helm registry login registry-1.docker.io`. Native Helm 3.8+ OCI push (NOT the
# chartmuseum cm-push plugin) — same as the sibling bborbe/agent and
# bborbe/recurring-task-creator charts.
CHART_OCI ?= oci://registry-1.docker.io/bborbe
.PHONY: helm-publish
helm-publish:
	@helm lint helm/
	@helm template smoke helm/ --set vaults[0].name=smoke --set vaults[0].repoUrl=git@example.com:smoke.git --set vaults[0].existingSecret=smoke >/dev/null || exit 1
	@helm package helm/ -d /tmp
	@helm push /tmp/git-rest-$$(awk '/^version:/{print $$2; exit}' helm/Chart.yaml).tgz $(CHART_OCI)

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
	go run github.com/incu6us/goimports-reviser/v3@$(GOIMPORTS_REVISER_VERSION) -project-name github.com/bborbe/git-rest -format -excludes vendor ./...
	find . -type d -name vendor -prune -o -type f -name '*.go' -print0 | xargs -0 -n 10 go run github.com/segmentio/golines@$(GOLINES_VERSION) --max-len=100 -w
	# golines last, then gofmt last so its wrapping is normalized and the gofmt lint check passes
	find . -type f -name '*.go' -not -path './vendor/*' -exec gofmt -w "{}" +

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
	go test -mod=mod -timeout 20m -p=$${GO_TEST_PARALLEL:-1} -cover $(TESTFLAGS_RACE) $(shell go list -mod=mod ./... | grep -v /vendor/)

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
                    GO-2026-5037 GO-2026-5038 GO-2026-5039 GO-2026-6599 GO-2026-6600 GO-2026-6603 GO-2026-6604 GO-2026-6605 GO-2026-6607 GO-2026-6608 GO-2026-6609 GO-2026-6610 GO-2026-6611 GO-2026-6612 GO-2026-6613 GO-2026-6617

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
ifeq ($(BUILD_TARGET),local)
build: check-version-tag check-go-mod
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
else
build:
	@if [ -z "$(REF)" ]; then \
		echo "ERROR: REF is required for a cluster build — pass REF=<commit>" >&2; \
		exit 1; \
	fi
	@if [ "$(origin VERSION)" = "file" ] || [ "$(origin VERSION)" = "undefined" ]; then \
		echo "ERROR: VERSION is not set, and the Makefile default is the latest release tag ($(VERSION))." >&2; \
		echo "  The cluster arm pushes, so building REF=$(REF) would overwrite the published" >&2; \
		echo "  image for that release tag with an image built from a different commit." >&2; \
		echo "  Set it explicitly: make build BUILD_TARGET=$(BUILD_TARGET) REF=$(REF) VERSION=<scratch>" >&2; \
		exit 1; \
	fi
	@case "$(VERSION)" in \
		v[0-9]*.[0-9]*.[0-9]*) \
			if [ "$(REF)" != "$(VERSION)" ]; then \
				echo "ERROR: refusing to push release tag $(VERSION) from REF=$(REF)." >&2; \
				echo "  A release image must be built from its own tag: REF=$(VERSION)." >&2; \
				exit 1; \
			fi ;; \
	esac
	BUILD_TARGET='$(BUILD_TARGET)' \
	NAMESPACE='build' \
	TIMEOUT='$(BUILD_TIMEOUT)' \
	REPO_URL='$(BUILD_REPO_URL)' \
	REF='$(REF)' \
	TAG='$(VERSION)' \
	DOCKERFILE='$(BUILD_DOCKERFILE)' \
	IMAGE='$(DOCKER_REGISTRY)/$(IMAGE)' \
	PUSH='true' \
	PUSH_SECRET='$(BUILD_PUSH_SECRET)' \
	hack/build-cluster.sh
endif

.PHONY: check-version-tag
check-version-tag:
	@if [ -n "$(ALLOW_UNTAGGED_BUILD)" ]; then \
		echo "ALLOW_UNTAGGED_BUILD set — skipping version/tag check"; \
	else \
		head_tag=$$(git describe --tags --exact-match HEAD 2>/dev/null); \
		if [ "$$head_tag" != "$(VERSION)" ]; then \
			echo "ERROR: refusing to build $(VERSION) from this tree." >&2; \
			echo "  HEAD is at tag: $${head_tag:-<untagged>}" >&2; \
			echo "  building as:    $(VERSION)" >&2; \
			echo "  An image stamped vX.Y.Z must be built from the vX.Y.Z tag." >&2; \
			echo "  Fix: git checkout $(VERSION)   (or set ALLOW_UNTAGGED_BUILD=1 for a scratch build)" >&2; \
			exit 1; \
		fi; \
	fi

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
ifeq ($(BUILD_TARGET),local)
	docker push $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION)
else
	@echo "upload: skipped — BUILD_TARGET=$(BUILD_TARGET); the cluster Job already pushed $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION)"
endif

.PHONY: clean
clean:
ifeq ($(BUILD_TARGET),local)
	docker rmi $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION) || true
	# docker builder prune -a -f
	docker builder prune --max-used-space 21474836480 -f || true
	rm -rf vendor
else
	@echo "clean: skipped — BUILD_TARGET=$(BUILD_TARGET); no local image to remove"
endif

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