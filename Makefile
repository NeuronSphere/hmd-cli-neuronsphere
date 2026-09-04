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

# Where `make install` puts the binary. ~/.local/bin is on PATH for a normal
# macOS or Linux login shell and needs no sudo; override for anywhere else.
PREFIX    ?= $(HOME)/.local/bin

.PHONY: all build generate install uninstall test test-verbose test-race cover vet fmt fmt-check check tidy clean run test-cli help

all: build

## generate: stage the bundled compose files into the Go module for go:embed
generate:
	@mkdir -p $(EMBED_DIR)
	@cp $(SERVICES_SRC)/docker-compose.control-plane.yml $(EMBED_DIR)/

## build: compile the binary to src/go/nsctl/build/nsctl
build: generate
	@mkdir -p $(BUILD_DIR)
	cd $(GO_DIR) && CGO_ENABLED=0 $(GO) build $(GOFLAGS) $(LDFLAGS) -o build/$(BINARY) .

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
	rm -rf $(BUILD_DIR) $(EMBED_DIR)

## run: build and print help (quick smoke test)
run: build
	$(BUILD_DIR)/$(BINARY) --help

## test-cli: build, then run the fast contract suite (no Docker)
test-cli: build
	$(ROBOT) $(ROBOT_FLAGS) test/nsctl_cli.robot

## help: show this help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
