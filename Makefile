BINARY  := pai
LDFLAGS := -s -w

.PHONY: build install test fmt vet deps clean

# SQLite session backend (pure Go, cgo-free).
build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/pai

install:
	go install -ldflags="$(LDFLAGS)" ./cmd/pai

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
