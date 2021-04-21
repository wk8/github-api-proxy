.DEFAULT_GOAL := all

SHELL := /bin/bash

.PHONY: all
all: test lint build

# the TEST_FLAGS env var can be set to eg run only specific tests
# the coverage output can be open with `go tool cover -html=coverage.out`
.PHONY: test
test: pkg/internal/mock_pkg/token_pool.go
	go test ./... -v -count=1 -race -cover -coverprofile=coverage.out "$$TEST_FLAGS"

.PHONY: lint
lint:
	golangci-lint run

.PHONY: build
build:
	go build -o github-api-proxy github.com/wk8/github-api-proxy/cmd

pkg/internal/mock_pkg:
	mkdir -p pkg/internal/mock_pkg

pkg/internal/mock_pkg/token_pool.go: ${GOPATH}/bin/mockgen pkg/internal/mock_pkg pkg/token-pools/token_pool.go
	mockgen -source pkg/token-pools/token_pool.go > pkg/internal/mock_pkg/token_pool.go

${GOPATH}/bin/mockgen:
	go install github.com/golang/mock/mockgen@v1.5.0
