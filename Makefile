GOLANGCI_LINT_VERSION := v2.12.2

.PHONY: build test lint lint-only fmt clean

build:
	go build -v -ldflags="-s -w" -o gr ./cmd/graith

test:
	go test -v -race ./...

lint:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run --fix

lint-only:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run

fmt:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint fmt

clean:
	rm -f gr
