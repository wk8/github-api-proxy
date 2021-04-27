FROM golang:1.15 AS builder

ARG VERSION=<UNKNOWN>

WORKDIR /go/src/github.com/wk8/github-api-proxy

COPY go.* ./
RUN go mod download

COPY . .
RUN go build -o github-api-proxy -ldflags="-w -s -X github.com/wk8/github-api-proxy/version.VERSION=${VERSION}" github.com/wk8/github-api-proxy/cmd

###

FROM alpine

# see https://stackoverflow.com/questions/36279253/go-compiled-binary-wont-run-in-an-alpine-docker-container-on-ubuntu-host/50861580#50861580
RUN apk update && apk add --no-cache libc6-compat

COPY --from=builder /go/src/github.com/wk8/github-api-proxy/github-api-proxy /usr/bin/github-api-proxy

ENTRYPOINT /usr/bin/github-api-proxy --config $CONFIG_FILE_PATH
