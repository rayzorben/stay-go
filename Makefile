BINARY   := stay-go
BUILD_DIR := bin
MODULE   := github.com/rayben/stay-go
VERSION_FILE := .build-version

.PHONY: build run test lint clean install

## build: compile the binary to bin/stay-go (embeds YYYYMMDD.nn from .build-version)
build:
	@V=$$( sh scripts/compute-build-version.sh "$(VERSION_FILE)" ); \
	go build -ldflags "-X main.version=$$V" -o $(BUILD_DIR)/$(BINARY) ./cmd/stay-go

## run: build and run with the default config
run: build
	./$(BUILD_DIR)/$(BINARY)

## dry-run: build and show the plan without executing
dry-run: build
	./$(BUILD_DIR)/$(BINARY) --dry-run

## debug: build and run with full command output
debug: build
	./$(BUILD_DIR)/$(BINARY) --debug

## test: run all unit tests
test:
	go test ./...

## test-verbose: run tests with full output
test-verbose:
	go test -v ./...

## cover: run tests and open coverage report
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

## lint: run go vet (add golangci-lint if available)
lint:
	go vet ./...
	@which golangci-lint >/dev/null 2>&1 && golangci-lint run || true

## install: install binary to /usr/local/bin
install: build
	install -m 755 $(BUILD_DIR)/$(BINARY) /usr/local/bin/$(BINARY)

## clean: remove build artifacts
clean:
	rm -rf $(BUILD_DIR) coverage.out

## help: list available targets
help:
	@grep -E '^## ' Makefile | sed 's/## //'
