BIN_DIR ?= ./bin
BIN     ?= $(BIN_DIR)/fine

COMMIT       := $(shell git rev-parse --short=7 HEAD 2>/dev/null || echo xxxxxxx)
DIRTY_FLAG   := $(shell git diff --quiet 2>/dev/null; echo $$?)
TAG          := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "")
BUILD_DATE   := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GO_VERSION   := $(shell go version 2>/dev/null | awk '{print $$3}')
VERSION_OVERRIDE ?=

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

DEV_LDFLAGS := -s -w \
    -X 'main.Version=dev-$(COMMIT)' \
    -X 'main.Commit=$(COMMIT)' \
    -X 'main.BuildDate=$(BUILD_DATE)' \
    -X 'main.GoVersionStr=$(GO_VERSION)'

.PHONY: build release dev run clean

build: release

release:
		@mkdir -p $(BIN_DIR)
		go build -tags netgo -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/fine

dev:
		@mkdir -p $(BIN_DIR)
		go build -tags netgo -ldflags "$(DEV_LDFLAGS)" -o $(BIN) ./cmd/fine

run: dev
	    ./$(BIN)

clean:
	    rm -rf $(BIN_DIR)
