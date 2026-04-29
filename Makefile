.PHONY: build test lint fmt clean install release

VERSION := $(shell git describe --tags --always 2>/dev/null || echo "dev")
# Single source of truth for the version string. Pre-v0.2.0 builds set
# main.version; that path is gone. Every consumer now reads
# internal/version.Current — see internal/version/version.go.
LDFLAGS := -X github.com/ersinkoc/dfmt/internal/version.Current=$(VERSION)

# Build targets
build:
	mkdir -p dist
	go build -ldflags "$(LDFLAGS)" -o dist/dfmt ./cmd/dfmt
	go build -ldflags "$(LDFLAGS)" -o dist/dfmt-bench ./cmd/dfmt-bench

test:
	go test ./...

lint:
	golangci-lint run ./...

fmt:
	go fmt ./...

clean:
	rm -rf dist/
	rm -rf .dfmt/

install: build
	cp dist/dfmt $(shell go env GOPATH)/bin/dfmt

release:
	@echo "Cross-compiling..."
	mkdir -p dist/release
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/release/dfmt-linux-amd64 ./cmd/dfmt
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/release/dfmt-linux-arm64 ./cmd/dfmt
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/release/dfmt-darwin-amd64 ./cmd/dfmt
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/release/dfmt-darwin-arm64 ./cmd/dfmt
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/release/dfmt-windows-amd64.exe ./cmd/dfmt
	GOOS=windows GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/release/dfmt-windows-arm64.exe ./cmd/dfmt
	GOOS=freebsd GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/release/dfmt-freebsd-amd64 ./cmd/dfmt
	@echo "Release binaries in dist/release/"
