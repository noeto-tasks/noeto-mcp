.DEFAULT_GOAL := help
.PHONY: help build test smoke lint fmt install docker docker-push

BIN     := noeto-mcp
GIT_SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo nogit)
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo $(GIT_SHA))

# GHCR rather than the ECR repository noeto-api pushes to: that one is private
# and exists to feed one deployment. This image is the distribution channel —
# it has to be publicly pullable by anyone who uses noeto, and it belongs beside
# the source in the same GitHub org.
IMAGE := ghcr.io/noeto-tasks/noeto-mcp

# Update via: curl -s https://api.github.com/repos/golangci/golangci-lint/releases/latest | grep '"tag_name"'
GOLANGCI_LINT_VERSION := v2.12.2
GOLANGCI_LINT := ./bin/golangci-lint

help: ## Show this help
	@grep -hE '^[a-z][a-zA-Z0-9_-]*:.*##' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}'

build: ## Build ./tmp/noeto-mcp
	@mkdir -p tmp
	go build -trimpath -ldflags="-X main.version=$(VERSION)" -o tmp/$(BIN) .
	@echo "==> tmp/$(BIN)  ($(VERSION))"

# Installs to GOBIN, which is the path that goes in the agent host's config.
# A relative path there breaks the moment the host is started from elsewhere.
install: ## Install noeto-mcp into GOBIN
	go install -trimpath -ldflags="-X main.version=$(VERSION)" .
	@echo "==> $$(go env GOBIN 2>/dev/null || echo $$(go env GOPATH)/bin)/$(BIN)"

test: ## Run the unit tests (hermetic — no API needed)
	go test ./...

# The drift check this repo needs because it lives outside noeto-api and so is
# not covered by that repo's `make openapi-check`. Reads only.
smoke: ## Check the API contract against a running noeto (needs NOETO_TOKEN)
	@: "$${NOETO_TOKEN:?set NOETO_TOKEN to a noeto_pat_ token — issue one under Settings → Access tokens}"
	go test ./internal/tools -run TestSmoke -count=1 -v

# Installed with `go install` rather than the upstream shell script that
# noeto-api uses. That script's checksum verification fails for the darwin/arm64
# v2.12.2 asset — the tarball it downloads hashes to c8debe3b… where the script
# expects a9c54498…, consistently, so it is a bad entry rather than a corrupted
# download. `go install` verifies through the Go checksum database instead,
# which is a different and at least as trustworthy path. Revisit if upstream
# fixes it; do not "solve" this by passing --no-verify to the script.
lint: ## Run golangci-lint
	@if [ ! -x $(GOLANGCI_LINT) ]; then \
		echo "golangci-lint not found — installing $(GOLANGCI_LINT_VERSION)"; \
		GOBIN=$(CURDIR)/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	fi
	$(GOLANGCI_LINT) run

fmt: ## Format the code and tidy the module
	gofmt -w .
	go mod tidy

# ── Container image ─────────────────────────────────────────────────────────
# The distribution channel: a user with Docker needs no Go, no Node, and no
# unquarantining — the config is `docker run -i --rm`, and stdio rides the pipe.

docker: ## Build the image locally for this machine's architecture
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .
	@echo "==> $(IMAGE):$(VERSION)"

# Both architectures, because the people this reaches are on Apple Silicon and
# on x86 in roughly equal numbers, and a single-arch image fails on the other
# with an exec-format error that says nothing about architecture.
#
# buildx pushes multi-arch directly; there is no local image to `docker push`
# afterwards, which is why this is one target and not two.
docker-push: ## Build and push a multi-arch image to GHCR (needs docker login ghcr.io)
	docker buildx build --platform linux/amd64,linux/arm64 \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) -t $(IMAGE):latest \
		--push .
	@echo "==> pushed $(IMAGE):$(VERSION) (amd64, arm64)"
