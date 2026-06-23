# Stamp the version from git so `nightshift --version` reflects the build.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build install test

build:
	go build -ldflags "$(LDFLAGS)" .

install:
	go install -ldflags "$(LDFLAGS)" .

test:
	go test ./...
