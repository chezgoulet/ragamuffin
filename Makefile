# Ragamuffin Makefile
#
# Convenience targets for local development builds. The primary build
# pipeline is the Dockerfile and GitHub Actions CI — this file exists
# to make local `go build` produce proper version metadata instead of
# "unknown" placeholders.
#
# Usage:
#   make build        — build for current OS/arch
#   make build-all    — build for all target platforms
#   make clean        — remove build artifacts

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
GOVER   ?= $(shell go version | cut -d' ' -f3 2>/dev/null || echo "unknown")

LDFLAGS := -s -w \
	-X 'github.com/chezgoulet/ragamuffin/internal/server.Version=$(VERSION)' \
	-X 'github.com/chezgoulet/ragamuffin/internal/server.Commit=$(COMMIT)' \
	-X 'github.com/chezgoulet/ragamuffin/internal/server.BuildDate=$(DATE)' \
	-X 'github.com/chezgoulet/ragamuffin/internal/server.GoVersion=$(GOVER)'

GOBUILD := CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)"

.PHONY: build build-all clean

build:
	$(GOBUILD) -o ragamuffin ./cmd/ragamuffin

build-all:
	GOOS=linux   GOARCH=amd64 $(GOBUILD) -o ragamuffin-linux-amd64   ./cmd/ragamuffin
	GOOS=linux   GOARCH=arm64 $(GOBUILD) -o ragamuffin-linux-arm64   ./cmd/ragamuffin
	GOOS=darwin  GOARCH=amd64 $(GOBUILD) -o ragamuffin-darwin-amd64  ./cmd/ragamuffin
	GOOS=darwin  GOARCH=arm64 $(GOBUILD) -o ragamuffin-darwin-arm64  ./cmd/ragamuffin

clean:
	rm -f ragamuffin ragamuffin-*-*
