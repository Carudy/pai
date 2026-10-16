BINARY  := pai
LDFLAGS := -s -w
# Where `make local` installs the built binary.
BINDIR  ?= $(HOME)/.local/bin

.PHONY: build install local test fmt vet deps clean

# SQLite session backend (pure Go, cgo-free).
build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/pai

install:
	go install -ldflags="$(LDFLAGS)" ./cmd/pai

# Build and copy the binary into BINDIR (default ~/.local/bin) for local use.
# `make local BINDIR=/usr/local/bin` to deploy elsewhere.
local: build
	install -d "$(BINDIR)"
	install -m 0755 $(BINARY) "$(BINDIR)/$(BINARY)"

# Pre-fill the module cache. Run once after changing dependencies: without it,
# the first build can stall for ~30s if the module proxy is unreachable. Uses
# plain "go mod download" (not "all") so go.sum stays identical to `go mod tidy`.
deps:
	go mod download

test:
	go test ./...

fmt:
	gofmt -l -w ./cmd ./internal

vet:
	go vet ./...

clean:
	rm -f $(BINARY)
