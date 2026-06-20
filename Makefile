# Fine — Discord moderation bot
#
# Build modes:
#   go build                (default — uses dev defaults baked into main.go)
#   make build              (release — uses ldflags with git tag + short sha + dirty suffix)
#   make dev                (dev — uses ldflags with dev-<sha>)
#   make run                (build dev + run)
#
# Examples:
#   make build -o ./bin/fine
#   make dev
#   VERSION_OVERRIDE=v9.9.9 make build   (force a synthetic tag)

BIN_DIR ?= ./bin
BIN     ?= $(BIN_DIR)/fine

COMMIT       := $(shell git rev-parse --short=7 HEAD 2>/dev/null || echo xxxxxxx)
DIRTY_FLAG   := $(shell git diff --quiet 2>/dev/null; echo $$?)
TAG          := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "")
BUILD_DATE   := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GO_VERSION   := $(shell go version 2>/dev/null | awk '{print $$3}')
VERSION_OVERRIDE ?=

# VERSION resolution:
#   - if VERSION_OVERRIDE is set, use it (CI scenario)
#   - else if a git tag exists, use <tag>-<commit>[-dirty]
#   - else use dev-<commit>[-dirty]
ifeq ($(VERSION_OVERRIDE),)
ifeq ($(TAG),)
VERSION := dev-$(COMMIT)
else
VERSION := $(TAG)-$(COMMIT)
endif
else
VERSION := $(VERSION_OVERRIDE)
endif

ifeq ($(DIRTY_FLAG),1)
VERSION := $(VERSION)-dirty
endif

LDFLAGS := -s -w \
    -X 'main.Version=$(VERSION)' \
    -X 'main.Commit=$(COMMIT)' \
    -X 'main.BuildDate=$(BUILD_DATE)' \
    -X 'main.GoVersionStr=$(GO_VERSION)'

.PHONY: build release dev run clean

build: release ## Alias for the release build (uses git tag when present).

release: ## Build with full version stamp; uses git tag if available, else dev-<sha>.
	@mkdir -p $(BIN_DIR)
	go build -tags netgo -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/fine

dev: ## Build with version=dev-<sha>; faster, smaller surface.
	@mkdir -p $(BIN_DIR)
	go build -tags netgo -ldflags "-s -w \
	    -X 'main.Version=dev-$(COMMIT)' \
	    -X 'main.Commit=$(COMMIT)' \
	    -X 'main.BuildDate=$(BUILD_DATE)' \
	    -X 'main.GoVersionStr=$(GO_VERSION)'" -o $(BIN) ./cmd/fine

run: dev ## Build the dev binary and run it.
	./$(BIN)

clean: ## Remove built artifacts.
	rm -rf $(BIN_DIR)
