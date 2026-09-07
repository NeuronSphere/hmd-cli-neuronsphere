BINARY    := nsctl
GO_DIR    := src/go/nsctl
BUILD_DIR := $(GO_DIR)/build

GO        := go
GOFLAGS   :=

VERSION   := $(shell cat meta-data/VERSION 2>/dev/null || echo "dev")
# -s -w drop the symbol table and DWARF. This is a distributed binary, not one
# anyone debugs in place, and they take roughly 20 MB off it.
LDFLAGS   := -ldflags "-X main.version=$(VERSION) -s -w"

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

# The DAG-runner image is not published; nsctl builds it on demand from this
# module's own sources, which have to travel inside the binary for a `brew
# install` to be able to. tools/runnersrc packs them deterministically -- the
# archive's digest is the image tag, so a tarball that churned would rebuild
# the image on every start.
RUNNER_EMBED_DIR := $(GO_DIR)/internal/bundled/runner

# The repo trees a deploy needs. A projectbuilder node bind-mounts the repo's
# working tree, so without these a Homebrew-installed nsctl cannot deploy an
# environment at all -- the substrate's first node fails looking for a tree
# under $HMD_REPO_HOME that a distributed binary has no reason to have.
#
# Sources, in order: the pre_build_artifacts destination `hmd build` populates,
# then the trees committed under bundled-repos/. The fallback is what lets
# `make generate` run on a clean CI checkout with no network and no
# credentials, which is where GoReleaser runs it.
REPOS_EMBED_DIR := $(GO_DIR)/internal/bundled/repos
ARTIFACTS_DIR   := src/python/hmd_cli_neuronsphere/external
BUNDLED_REPOS_DIR := bundled-repos

# The control plane's own instances, the environment substrate, the foundation
# services, and the two operators every cloud chart's ExternalSecrets need.
# Not the workloads: superset, hyperdx, telemetry-debug and s3bucket are
# plugin content and are deliberately absent.
BUNDLED_REPOS := hmd-vpc hmd-postgres-rds hmd-inf-neptune hmd-inf-eks-cluster \
                 hmd-ms-naming hmd-ms-artifact-lib hmd-ms-deployment hmd-ms-dbaccount \
                 hmd-inf-ext-secrets-crds hmd-inf-ext-secrets

# Where `make install` puts the binary. ~/.local/bin is on PATH for a normal
# macOS or Linux login shell and needs no sudo; override for anywhere else.
PREFIX    ?= $(HOME)/.local/bin

# The DAG-runner image built by `make image`. Local, always: the image is not
# published to any registry, so every tag that names it is one built here.
NSRUNNER_IMAGE ?= hmd-img-nsrunner:$(VERSION)

.PHONY: test-parity all build generate generate-verbose refresh-bundled-repos image install uninstall test test-verbose test-race cover vet fmt fmt-check check tidy clean run test-cli help

all: build

## generate: stage the bundled compose files and runner sources for go:embed
generate:
	@mkdir -p $(EMBED_DIR)
	@cp $(SERVICES_SRC)/docker-compose.control-plane.yml $(EMBED_DIR)/
	@mkdir -p $(RUNNER_EMBED_DIR)
	@cd $(GO_DIR) && $(GO) run ./tools/runnersrc . >/dev/null
	@mkdir -p $(REPOS_EMBED_DIR)
	@cd $(GO_DIR) && $(GO) run ./tools/repopack \
	  -out internal/bundled/repos \
	  -artifacts ../../../$(ARTIFACTS_DIR) \
	  -fallback ../../../$(BUNDLED_REPOS_DIR) \
	  $(BUNDLED_REPOS) >/dev/null

## generate-verbose: the same, printing what each bundled repo tree costs
generate-verbose:
	@$(MAKE) generate
	@cd $(GO_DIR) && $(GO) run ./tools/repopack \
	  -out internal/bundled/repos \
	  -artifacts ../../../$(ARTIFACTS_DIR) \
	  -fallback ../../../$(BUNDLED_REPOS_DIR) \
	  $(BUNDLED_REPOS)

## refresh-bundled-repos: re-copy the committed repo trees from $HMD_REPO_HOME
#
# Run by a person, and its output is committed. Deliberately not part of
# `generate`: a release build must not depend on ten checkouts being present,
# and a tree that silently changed under a build would make two binaries of the
# same version deploy different code.
refresh-bundled-repos:
	@test -n "$(HMD_REPO_HOME)" || { echo "set HMD_REPO_HOME to where the repos are checked out"; exit 2; }
	@for repo in $(BUNDLED_REPOS); do \
	  src="$(HMD_REPO_HOME)/$$repo"; \
	  test -d "$$src" || { echo "missing $$src"; exit 1; }; \
	  rm -rf "$(BUNDLED_REPOS_DIR)/$$repo"; \
	  mkdir -p "$(BUNDLED_REPOS_DIR)/$$repo"; \
	  for top in meta-data src; do \
	    test -d "$$src/$$top" || continue; \
	    rsync -a --delete \
	      --exclude '.git' --exclude 'build' --exclude 'target' --exclude 'dist' \
	      --exclude 'node_modules' --exclude '__pycache__' --exclude '*.egg-info' \
	      --exclude '.terraform' --exclude 'cdktf.out' --exclude '.pytest_cache' \
	      --exclude 'resources_output' \
	      "$$src/$$top" "$(BUNDLED_REPOS_DIR)/$$repo/"; \
	  done; \
	  echo "  refreshed $$repo"; \
	done

## build: compile the binary to src/go/nsctl/build/nsctl
build: generate
	@mkdir -p $(BUILD_DIR)
	cd $(GO_DIR) && CGO_ENABLED=0 $(GO) build $(GOFLAGS) $(LDFLAGS) -o build/$(BINARY) .

## image: build the nsrunner container image (the same binary, serving)
#
# `nsctl control-plane start` builds this itself from the embedded sources when
# the runner is enabled and the image is absent. This target is the same build
# from the working tree, for iterating on the runner without a control plane.
image: generate
	cd $(GO_DIR) && docker build --build-arg VERSION=$(VERSION) -t $(NSRUNNER_IMAGE) .
	@echo "built $(NSRUNNER_IMAGE)"

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

## tidy: tidy and verify go modules
tidy:
	cd $(GO_DIR) && $(GO) mod tidy && $(GO) mod verify

## clean: remove build artifacts and staged files
clean:
	rm -rf $(BUILD_DIR) $(EMBED_DIR) $(RUNNER_EMBED_DIR) $(REPOS_EMBED_DIR)

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
# It starts and stops a real environment in the HMD_HOME it is given, so it asks
# for that in writing. HMD_HOME alone is not consent -- it is set in every shell
# anyone works in, which makes `make test-parity` far too easy to run at the
# wrong platform by reflex. NSCTL_PARITY_ENV names the environment to use and
# has no default for the same reason.
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
