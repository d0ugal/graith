GOLANGCI_LINT_VERSION := v2.12.2

.PHONY: build test lint lint-only lint-darwin fmt clean

build:
	go build -v -ldflags="-s -w" -o gr ./cmd/graith

test:
	go test -v -race ./...

lint:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run --fix

lint-only:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run

# Lint with GOOS=darwin so Darwin-only files (e.g. *_darwin.go) are compiled and
# checked. CI lints on Linux, which never sees these files (issue #784).
lint-darwin:
	docker run --rm -e GOOS=darwin -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run

fmt:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint fmt

clean:
	rm -f gr
