BINARY  := resticle
PKG     := ./cmd/resticle
DIST    := dist
PREFIX  := /usr/local

# Static, CGO-free: the backup host gets one file with no runtime deps.
HOST_ENV := CGO_ENABLED=0 GOOS=linux GOARCH=amd64
GOFLAGS  := -trimpath

# `git describe` on a semver tag gives v1.2.3; between tags it appends the
# commit, and a dirty tree is marked as such — so a binary always says where
# it came from. VERSION and BUILD_TIME can be overridden (the release
# workflow passes the tag).
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
STAMP     := -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)
LDFLAGS   := -s -w $(STAMP)

.PHONY: all build dist test race check fmt vet goldens install clean help

all: build

build: ## build ./resticle for this machine
	go build $(GOFLAGS) -ldflags="$(STAMP)" -o $(BINARY) $(PKG)

dist: ## build the static linux/amd64 binary for the backup host
	@mkdir -p $(DIST)
	$(HOST_ENV) go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY) $(PKG)
	@sha256sum $(DIST)/$(BINARY)

test: ## run the test suite
	go test ./...

race: ## run the test suite under the race detector
	go test -race ./...

fmt: ## fail if anything is unformatted
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet: ## run go vet
	go vet ./...

check: fmt vet test ## everything that must pass before a commit

# Regenerates testdata/argv/*.txt. Any diff there is a change in the restic
# command lines resticle produces — read it before committing.
goldens: ## rewrite the argv snapshot files
	go test ./internal/restic/ -run TestArgvSnapshot -update
	@git diff --stat -- testdata/argv || true

install: build ## install to /usr/local/bin (override PREFIX; use sudo)
	install -m 755 $(BINARY) $(PREFIX)/bin/$(BINARY)

clean: ## remove build output
	rm -rf $(BINARY) $(DIST)

help: ## list targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk -F':.*?## ' '{printf "  %-8s %s\n", $$1, $$2}'
