BINARY    := nsctl
GO_DIR    := src/go/nsctl
BUILD_DIR := $(GO_DIR)/build

GO        := go
GOFLAGS   :=

VERSION   := $(shell cat meta-data/VERSION 2>/dev/null || echo "dev")
# The endpoint a build signs in against when nothing is configured. Empty here
# on purpose: a development build has no hosted tenant to name, and a constant
# pointing at a host that does not answer is worse than `nsctl login` refusing
# and saying what to write. A release sets it -- see .goreleaser.yaml.
NSCTL_DEFAULT_AUTH_URL ?=
NSCONFIG  := github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig

# -s -w drop the symbol table and DWARF. This is a distributed binary, not one
# anyone debugs in place, and they take roughly 20 MB off it.
LDFLAGS   := -ldflags "-X main.version=$(VERSION) -X $(NSCONFIG).DefaultAuthURL=$(NSCTL_DEFAULT_AUTH_URL) -s -w"

# Robot Framework is optional and not a Go dependency. Override ROBOT to run it
# without installing it, e.g.
#   make test-robot ROBOT="uvx --from robotframework robot"
ROBOT         ?= python3 -m robot

# The Python front end, for the parity suite. Override if `hmd` is not on PATH.
HMD_BIN       ?= hmd
ROBOT_RESULTS := test/results
ROBOT_TS      := $(shell date -u +%Y-%m-%dT%H%M%S)
ROBOT_FLAGS    = --variable BINARY:$(abspath $(BUILD_DIR)/$(BINARY)) \
                 --outputdir $(ROBOT_RESULTS) \
                 --output  $(ROBOT_TS)-output.xml \
                 --log     $(ROBOT_TS)-log.html \
                 --report  $(ROBOT_TS)-report.html

# The compose file the binary embeds lives in the Python package, which also
# reads it. Staging rather than duplicating keeps one source of truth; go:embed
# cannot reach outside its own module, so it has to be copied in.
SERVICES_SRC := src/python/hmd_cli_neuronsphere/services
EMBED_DIR    := $(GO_DIR)/internal/bundled/services

# The nsctl image (the local identity provider, authd, runs from it) is not
# published; nsctl builds it on demand from this module's own sources, which
# have to travel inside the binary for a `brew install` to be able to.
# tools/nsctlsrc packs them deterministically -- the archive's digest is the
# image tag, so a tarball that churned would rebuild the image on every start.
IMAGE_EMBED_DIR := $(GO_DIR)/internal/bundled/image

# The repo trees a deploy needs. A projectbuilder node bind-mounts the repo's
# working tree, so without these a Homebrew-installed nsctl cannot deploy an
# environment at all -- the substrate's first node fails looking for a tree
# under $HMD_REPO_HOME that a distributed binary has no reason to have.
#
# Every tree comes from the repo class's published `build` artifact, pinned in
# meta-data/manifest.json as a BACON pre_build_artifact. repopack takes each
# from the pre_build_artifacts destination `hmd build` populates, then from its
# own fetch cache, then from the artifact librarian directly -- which is what
# lets `make generate` run on a CI runner with no `hmd` installed.
#
# The fetch cache is deliberately not the pre_build_artifacts destination:
# `hmd build` rmtree's that when it finishes, so a cache living there would be
# wiped by an ordinary Python build.
REPOS_EMBED_DIR := $(GO_DIR)/internal/bundled/repos
ARTIFACTS_DIR   := src/python/hmd_cli_neuronsphere/external
ARTIFACT_CACHE  := .artifacts

# The control plane's own instances, the environment substrate, the foundation
# services, and the two operators every cloud chart's ExternalSecrets need.
# Not the workloads: superset, hyperdx, telemetry-debug and s3bucket are
# plugin content and are deliberately absent, though the manifest pins them
# for the Python CLI.
BUNDLED_REPOS := hmd-vpc hmd-postgres-rds hmd-inf-neptune hmd-inf-eks-cluster \
                 hmd-ms-naming hmd-ms-artifact-lib hmd-ms-deployment-core hmd-ms-dbaccount \
                 hmd-inf-ext-secrets-crds hmd-inf-ext-secrets

# Where `make install` puts the binary. ~/.local/bin is on PATH for a normal
# macOS or Linux login shell and needs no sudo; override for anywhere else.
PREFIX    ?= $(HOME)/.local/bin

# The nsctl image built by `make image`. Local, always: the image is not
# published to any registry, so every tag that names it is one built here.
NSCTL_IMAGE ?= hmd-img-nsctl:$(VERSION)

.PHONY: test-parity all build generate generate-verbose image install uninstall test test-verbose test-race cover vet fmt fmt-check check tidy clean clean-artifacts run test-cli docs-reference docs-reference-check help

all: build

## generate: stage the bundled compose files, nsctl sources and repo trees
generate:
	@mkdir -p $(EMBED_DIR)
	@cp $(SERVICES_SRC)/docker-compose.control-plane.yml $(EMBED_DIR)/
	@mkdir -p $(IMAGE_EMBED_DIR)
	@cd $(GO_DIR) && $(GO) run ./tools/nsctlsrc . >/dev/null
	@mkdir -p $(REPOS_EMBED_DIR)
	@cd $(GO_DIR) && $(GO) run ./tools/repopack \
	  -out internal/bundled/repos \
	  -manifest ../../../meta-data/manifest.json \
	  -artifacts ../../../$(ARTIFACTS_DIR) \
	  -cache $(ARTIFACT_CACHE) \
	  $(BUNDLED_REPOS) >/dev/null

## generate-verbose: the same, printing where each bundled repo tree came from
generate-verbose:
	@$(MAKE) generate
	@cd $(GO_DIR) && $(GO) run ./tools/repopack \
	  -out internal/bundled/repos \
	  -manifest ../../../meta-data/manifest.json \
	  -artifacts ../../../$(ARTIFACTS_DIR) \
	  -cache $(ARTIFACT_CACHE) \
	  $(BUNDLED_REPOS)

## build: compile the binary to src/go/nsctl/build/nsctl
build: generate
	@mkdir -p $(BUILD_DIR)
	cd $(GO_DIR) && CGO_ENABLED=0 $(GO) build $(GOFLAGS) $(LDFLAGS) -o build/$(BINARY) .

## image: build the nsctl container image (the binary, serving authd)
#
# `nsctl control-plane start` builds this itself from the embedded sources when
# the identity provider is enabled and the image is absent. This target is the
# same build from the working tree, for iterating without a control plane.
image: generate
	cd $(GO_DIR) && docker build --build-arg VERSION=$(VERSION) -t $(NSCTL_IMAGE) .
	@echo "built $(NSCTL_IMAGE)"

## install: build and copy the binary to $(PREFIX)
install: build
	@mkdir -p $(PREFIX)
	@install -m 0755 $(BUILD_DIR)/$(BINARY) $(PREFIX)/$(BINARY)
	@echo "installed $(PREFIX)/$(BINARY) ($$($(PREFIX)/$(BINARY) version))"

## uninstall: remove the installed binary
uninstall:
	@rm -f $(PREFIX)/$(BINARY)
	@echo "removed $(PREFIX)/$(BINARY)"

## test: run the Go unit tests
test: generate
	cd $(GO_DIR) && $(GO) test $(GOFLAGS) ./...

## test-verbose: run the Go unit tests with verbose output
test-verbose: generate
	cd $(GO_DIR) && $(GO) test $(GOFLAGS) -v ./...

## test-race: run the Go unit tests under the race detector
test-race: generate
	cd $(GO_DIR) && $(GO) test $(GOFLAGS) -race -count=1 ./...

## cover: run the Go unit tests with coverage
cover: generate
	cd $(GO_DIR) && $(GO) test $(GOFLAGS) -cover ./...

## vet: run go vet
vet: generate
	cd $(GO_DIR) && $(GO) vet ./...

## fmt: format the Go sources
fmt:
	cd $(GO_DIR) && $(GO) fmt ./...

## fmt-check: fail if any Go source is not gofmt-clean
fmt-check:
	@cd $(GO_DIR) && out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

## check: fmt-check, vet and test -- the CI target
check: fmt-check vet test

## docs-reference: regenerate the Cobra command reference
docs-reference:
	cd $(GO_DIR) && $(GO) run ./tools/docref -out ../../../docs/reference/commands.rst

## docs-reference-check: fail when the checked-in command reference is stale
docs-reference-check:
	@tmp=$$(mktemp); \
	cd $(GO_DIR) && $(GO) run ./tools/docref -out "$$tmp" && \
	diff -u ../../../docs/reference/commands.rst "$$tmp"; \
	status=$$?; rm -f "$$tmp"; exit $$status

## tidy: tidy and verify go modules
tidy:
	cd $(GO_DIR) && $(GO) mod tidy && $(GO) mod verify

## clean: remove build artifacts and staged files
#
# Leaves the fetch cache alone. It is keyed <class>@<version>, so it is only
# ever stale when a pin moves, and discarding it turns every `make clean` into
# ten downloads.
clean:
	rm -rf $(BUILD_DIR) $(EMBED_DIR) $(RUNNER_EMBED_DIR) $(REPOS_EMBED_DIR)

## clean-artifacts: also discard the fetched repo trees, forcing a refetch
clean-artifacts: clean
	rm -rf $(GO_DIR)/$(ARTIFACT_CACHE)

## run: build and print help (quick smoke test)
run: build
	$(BUILD_DIR)/$(BINARY) --help

## test-cli: build, then run the fast contract suite (no Docker)
test-cli: build
	$(ROBOT) $(ROBOT_FLAGS) test/nsctl_cli.robot

## test-parity: SPEC012's parity suite -- needs Docker, a platform and an HMD_HOME
#
# Deliberately not part of `check`, CI or test-cli: it performs an operation
# with one front end and verifies it with the other, so it needs both installed
# and a real platform to operate on.
#
# It starts, stops and PURGES a real environment in the HMD_HOME it is given, so
# it asks for that in writing. HMD_HOME alone is not consent -- it is set in
# every shell anyone works in, which makes `make test-parity` far too easy to run
# at the wrong platform by reflex. NSCTL_PARITY_ENV names the environment to use
# and has no default for the same reason.
#
# The two purge tests each destroy the environment and rebuild it, so a run
# costs two cold starts on top of the start/stop pair. That cost is deliberate
# and recorded rather than discovered: purge is the verb where a leftover is the
# entire failure mode, and the first real run of it left seven containers, three
# volumes and the platform network behind.
test-parity: build
	@test -n "$(HMD_HOME)" || { echo "export HMD_HOME: this suite operates on a real platform"; exit 2; }
	@test -n "$(NSCTL_PARITY_ENV)" || { \
	  echo "This suite STARTS AND STOPS a real environment in $(HMD_HOME)."; \
	  echo "Name the environment to use it on:"; \
	  echo "    make test-parity NSCTL_PARITY_ENV=<slug>"; \
	  exit 2; }
	$(ROBOT) $(ROBOT_FLAGS) --variable HMD:$(HMD_BIN) --variable ENV:$(NSCTL_PARITY_ENV) \
	  test/nsctl_parity.robot

## help: show this help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
