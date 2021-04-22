.DEFAULT_GOAL := all

SHELL := /usr/bin/env bash

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

DEV_DB_CONTAINER_NAME = github-api-proxy-mysql-dev

DEV_DB_NAME = github_api_proxy_dev
DEV_DB_PASSWORD = password

.PHONY: dev_db_start
dev_db_start:
	docker run --name $(DEV_DB_CONTAINER_NAME) --security-opt seccomp:unconfined -p 3306:3306 -e MYSQL_ROOT_PASSWORD=$(DEV_DB_PASSWORD) -d --rm mysql --default-authentication-plugin=mysql_native_password
	@# wait for it to come up
	@for _ in $$(seq 30); do \
		docker exec $(DEV_DB_CONTAINER_NAME) mysql -p$(DEV_DB_PASSWORD) -e 'SELECT 1' &> /dev/null && echo 'MySql up' && exit 0; \
		sleep 1; \
	done; \
	echo 'Timed out waiting for MySql to get up' && exit 1
	docker exec $(DEV_DB_CONTAINER_NAME) mysql -p$(DEV_DB_PASSWORD) -e "CREATE DATABASE IF NOT EXISTS $(DEV_DB_NAME) DEFAULT CHARACTER SET = 'utf8' DEFAULT COLLATE 'utf8_general_ci'"

.PHONY: dev_db_stop
dev_db_stop:
	docker kill $(DEV_DB_CONTAINER_NAME)
