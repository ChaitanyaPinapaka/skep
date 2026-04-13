VERSION := 0.1.0
BINARY := skep
LDFLAGS := -s -w

.PHONY: build test clean release release-snapshot release-dry-run install help

# Build for current platform
build:
	CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/skep/

# Run all tests
test:
	CGO_ENABLED=1 go test ./... -timeout 60s

# Clean build artifacts
clean:
	rm -f $(BINARY) skep-*

# Build release binaries for all platforms
# Requires cross-compilation toolchains (use CI or GoReleaser)
release: clean
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o skep-linux-amd64 ./cmd/skep/
	GOOS=linux GOARCH=arm64 CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc go build -ldflags "$(LDFLAGS)" -o skep-linux-arm64 ./cmd/skep/
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o skep-darwin-amd64 ./cmd/skep/
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o skep-darwin-arm64 ./cmd/skep/

# Install to $GOPATH/bin
install:
	CGO_ENABLED=1 go install -ldflags "$(LDFLAGS)" ./cmd/skep/

# GoReleaser: local snapshot build (no publish, artifacts in ./dist)
release-snapshot:
	goreleaser release --snapshot --clean --skip=publish

# GoReleaser: validate config without building or tagging
release-dry-run:
	goreleaser check

# Show help
help:
	@echo "make build             Build for current platform"
	@echo "make test              Run all tests"
	@echo "make install           Install to GOPATH/bin"
	@echo "make release           Build release binaries (needs cross-compilers)"
	@echo "make release-snapshot  GoReleaser local snapshot build (no publish)"
	@echo "make release-dry-run   Validate .goreleaser.yml"
	@echo "make clean             Remove build artifacts"
