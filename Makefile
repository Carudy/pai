BINARY  := pai
LDFLAGS := -s -w

.PHONY: build build-file install test fmt vet clean

# Default: SQLite session backend (pure Go, cgo-free).
build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/pai

# JSONL session backend: no SQLite dependency, ~4 MB smaller.
build-file:
	CGO_ENABLED=0 go build -tags filestore -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/pai

install:
	go install -ldflags="$(LDFLAGS)" ./cmd/pai

test:
	go test ./... && go test -tags filestore ./...

fmt:
	gofmt -l -w ./cmd ./internal

vet:
	go vet ./... && go vet -tags filestore ./...

clean:
	rm -f $(BINARY)
