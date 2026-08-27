.DEFAULT_GOAL := help
.PHONY: help build test smoke lint fmt install docker docker-push release release-dry release-plugin

# Optional local settings, for the tokens the publishing targets need and for
# anything else you would rather not retype: GITHUB_TOKEN for `make release`,
# NOETO_TOKEN for `make smoke`.
#
# Read by make rather than by a shell, so the lines are plain KEY=value — no
# `export`, no quotes, and a `#` anywhere starts a comment. `export` on its own
# line passes them to the recipes. Read first so a VERSION set here wins over
# the `?=` below.
#
# Gitignored and kept out of the build context. It holds a token that can
# publish under your name, in plain text, which is the trade for not typing it.
-include .env
export

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

# Update via: curl -s https://api.github.com/repos/goreleaser/goreleaser/releases/latest | grep '"tag_name"'
GORELEASER_VERSION := v2.17.1
GORELEASER := ./bin/goreleaser

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

# ── Release ─────────────────────────────────────────────────────────────────
# The channel for the people the other two miss: the image needs Docker and
# `make install` needs a Go toolchain and a clone. This one needs neither —
# a prebuilt binary from a GitHub Release, or `brew install`.
#
# Run from a laptop after tagging. There is no CI in this repo, and the tag is
# what names the artifacts and stamps the version into the binary.

$(GORELEASER):
	@echo "goreleaser not found — installing $(GORELEASER_VERSION)"
	GOBIN=$(CURDIR)/bin go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

release-dry: $(GORELEASER) ## Build the release artifacts into dist/ without publishing
	$(GORELEASER) release --snapshot --clean --skip=publish

# goreleaser refuses to run without a tag on HEAD and a clean tree, which is the
# behaviour we want: an artifact named after a commit it was not built from is
# worse than no artifact. GITHUB_TOKEN needs `repo` scope — the release lands
# here and the cask is committed to noeto-tasks/homebrew-tap.
release: $(GORELEASER) ## Publish a tagged release to GitHub Releases + Homebrew tap
	@: "$${GITHUB_TOKEN:?set GITHUB_TOKEN to a token with repo scope}"
	$(GORELEASER) release --clean

# ── Plugin release ──────────────────────────────────────────────────────────
# Shipping a plugin version by hand is four things that have to agree: the image
# tag pinned in .mcp.json and the README, the version in both manifests, the git
# tag, and what is actually in GHCR. This target is the one command that keeps
# them in step.
#
# RELEASE is plain semver — 0.2.1. The git tag and the image tag carry a `v`,
# the two plugin manifests do not; that mismatch is the whole reason this is a
# target and not a sed one-liner in someone's shell history.
#
# The order is the point. Everything local and reversible runs first, and the
# image is pushed *before* the commit that pins it becomes public, so no failure
# can leave the plugin pinned to a tag that does not exist in GHCR. If a step
# after the commit fails, undo with:
#   git tag -d v<version> && git reset --hard HEAD~1

PLUGIN_MANIFEST      := plugins/noeto/.claude-plugin/plugin.json
MARKETPLACE_MANIFEST := .claude-plugin/marketplace.json
PINNED_FILES         := plugins/noeto/.mcp.json README.md

release-plugin: $(GORELEASER) ## Ship a plugin version end to end (RELEASE=0.2.1)
	@[ -n "$(RELEASE)" ] || { echo "set RELEASE to the new version, without a leading v — e.g. make release-plugin RELEASE=0.2.1"; exit 1; }
	@echo "$(RELEASE)" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "RELEASE must be plain semver with no leading v, got: $(RELEASE)"; exit 1; }
	@[ -z "$$(git status --porcelain)" ] || { echo "working tree is dirty — commit or stash first"; exit 1; }
	@[ "$$(git rev-parse --abbrev-ref HEAD)" = "main" ] || { echo "not on main — this repo releases from main"; exit 1; }
	@! git rev-parse -q --verify "refs/tags/v$(RELEASE)" >/dev/null || { echo "tag v$(RELEASE) already exists"; exit 1; }
# Checked here rather than in `release`, where it would fail after the image and
# the commit are already public and there is nothing left to undo cheaply.
	@: "$${GITHUB_TOKEN:?set GITHUB_TOKEN to a token with repo scope}"
	$(MAKE) test lint
# perl, not `sed -i`, because the in-place flag takes an argument on BSD and not
# on GNU, and this runs on both.
	IMG='$(IMAGE)' TAG='v$(RELEASE)' perl -pi -e 's/\Q$$ENV{IMG}\E:[\w.-]+/$$ENV{IMG}:$$ENV{TAG}/g' $(PINNED_FILES)
	VER='$(RELEASE)' perl -pi -e 's/("version"\s*:\s*)"[^"]*"/$$1"$$ENV{VER}"/' $(PLUGIN_MANIFEST) $(MARKETPLACE_MANIFEST)
	@if command -v claude >/dev/null 2>&1; then claude plugin validate .; \
	else echo "claude not on PATH — skipping manifest validation"; fi
	git add $(PINNED_FILES) $(PLUGIN_MANIFEST) $(MARKETPLACE_MANIFEST)
	git commit -m "release: plugin v$(RELEASE)"
	git tag -a "v$(RELEASE)" -m "v$(RELEASE)"
# VERSION is passed explicitly: it defaults to `git describe --exact-match`,
# which is right here, but leaving the image tag to a default at the one moment
# it must match the pin is how the pin and the image drift apart.
	$(MAKE) docker-push VERSION=v$(RELEASE)
	git push origin main --follow-tags
	$(MAKE) release
	@if command -v claude >/dev/null 2>&1; then \
		claude plugin marketplace update noeto-mcp; \
		claude plugin update noeto@noeto-mcp; \
		echo "==> restart Claude Code to load noeto v$(RELEASE)"; \
	else echo "claude not on PATH — update the plugin by hand"; fi
