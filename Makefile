.PHONY: build test vet check clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-blueprint)

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/ore ./cmd/ore

test:
	CGO_ENABLED=0 go test ./...

vet:
	CGO_ENABLED=0 go vet ./...

check: vet test build

clean:
	rm -rf bin
