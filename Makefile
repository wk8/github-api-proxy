.DEFAULT_GOAL := all

SHELL := /usr/bin/env bash

.PHONY: all
all: test lint build image_build

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

######################
### Dev DB section ###
######################

DEV_DB_CONTAINER_NAME ?= github-api-proxy-mysql-dev

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

############################
### Docker image section ###
############################

IMG_REPO ?= wk88/github-api-proxy

.PHONY: image_build
image_build:
	@ IMG_NAME='$(IMG_REPO)' && VERSION='<UNKNOWN>' \
		&& if [ "$$GITHUB_API_PROXY_VERSION" ]; then IMG_NAME+=":$$GITHUB_API_PROXY_VERSION" && VERSION="$$GITHUB_API_PROXY_VERSION"; fi \
		&& docker build . --build-arg VERSION="$$VERSION" -t "$$IMG_NAME"


.PHONY: image_push
image_push:
	@ IMG_NAME='$(IMG_REPO)' && if [ "$$GITHUB_API_PROXY_VERSION" ]; then IMG_NAME+=":$$GITHUB_API_PROXY_VERSION"; fi \
		&& docker push "$$IMG_NAME"

#######################
### Release section ###
#######################

GITHUB_USER ?= wk8
GITHUB_REPO ?= github-api-proxy

.PHONY: release
release: _github_release
	@ git push && if [[ "$$(git status --porcelain)" ]]; then echo 'Working dir dirty, aborting' && exit 1; fi

	@ if [ ! "$$GITHUB_API_PROXY_TAG" ]; then echo 'GITHUB_API_PROXY_TAG env var not set, aborting' && exit 1; fi

	export GITHUB_API_PROXY_VERSION="$$GITHUB_API_PROXY_TAG-$$(git rev-parse HEAD)" \
		&& LDFLAGS="-w -s -X github.com/wk8/github-api-proxy/version.VERSION=$$GITHUB_API_PROXY_VERSION" \
		&& GOOS=darwin GOARCH=amd64 go build -o github-api-proxy-osx-amd64 -ldflags="$$LDFLAGS" github.com/wk8/github-api-proxy/cmd \
		&& GOOS=linux GOARCH=amd64 go build -o github-api-proxy-linux-amd64 -ldflags="$$LDFLAGS" github.com/wk8/github-api-proxy/cmd \
		&& GOOS=linux GOARCH=arm64 go build -o github-api-proxy-linux-arm64 -ldflags="$$LDFLAGS" github.com/wk8/github-api-proxy/cmd \
		&& $(MAKE) image_build image_push

	git tag "$$GITHUB_API_PROXY_TAG" && git push --tags

	github-release release --user $(GITHUB_USER) --repo $(GITHUB_REPO) --tag "$$GITHUB_API_PROXY_TAG"
	github-release upload --user $(GITHUB_USER) --repo $(GITHUB_REPO) --tag "$$GITHUB_API_PROXY_TAG" --file github-api-proxy-osx-amd64 --name github-api-proxy-osx-amd64
	github-release upload --user $(GITHUB_USER) --repo $(GITHUB_REPO) --tag "$$GITHUB_API_PROXY_TAG" --file github-api-proxy-linux-amd64 --name github-api-proxy-linux-amd64
	github-release upload --user $(GITHUB_USER) --repo $(GITHUB_REPO) --tag "$$GITHUB_API_PROXY_TAG" --file github-api-proxy-linux-arm64 --name github-api-proxy-linux-arm64

# see https://github.com/github-release/github-release
.PHONY: _github_release
_github_release:
	@ which github-release &> /dev/null || go get -u github.com/github-release/github-release
	@ if [ ! "$$GITHUB_TOKEN" ]; then echo 'GITHUB_TOKEN env var not set, aborting' && exit 1; fi
