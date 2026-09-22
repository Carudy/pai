BINARY  := pai
LDFLAGS := -s -w

.PHONY: build build-sqlite install test fmt vet clean

# Default: JSONL session backend, no extra dependencies (light, cgo-free).
build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/pai

# SQLite session backend (pure Go, ~4 MB larger).
build-sqlite:
	CGO_ENABLED=0 go build -tags sqlite -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/pai

install:
	go install -ldflags="$(LDFLAGS)" ./cmd/pai

test:
	go test ./... && go test -tags sqlite ./...

fmt:
	gofmt -l -w ./cmd ./internal

vet:
	go vet ./... && go vet -tags sqlite ./...

clean:
	rm -f $(BINARY)
