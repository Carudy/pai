BINARY  := pai
LDFLAGS := -s -w

.PHONY: build build-file install test fmt vet deps clean

# Default: SQLite session backend (pure Go, cgo-free).
build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/pai

# JSONL session backend: no SQLite dependency, ~4 MB smaller.
build-file:
	CGO_ENABLED=0 go build -tags filestore -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/pai

install:
	go install -ldflags="$(LDFLAGS)" ./cmd/pai

# Pre-fill the module cache. Run once after changing dependencies: without it,
# the first build can stall for ~30s if the module proxy is unreachable. Uses
# plain "go mod download" (not "all") so go.sum stays identical to `go mod tidy`.
deps:
	go mod download

test:
	go test ./... && go test -tags filestore ./...

fmt:
	gofmt -l -w ./cmd ./internal

vet:
	go vet ./... && go vet -tags filestore ./...

clean:
	rm -f $(BINARY)
