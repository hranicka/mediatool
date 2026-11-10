.PHONY: all install test build lint fmt release-check

STATICCHECK := $(shell command -v staticcheck 2>/dev/null || echo "$(shell go env GOPATH)/bin/staticcheck")
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

all: test build

install:
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go mod download

test:
	go vet ./...
	$(STATICCHECK) ./...
	go test -race ./...

build:
	mkdir -p ./dist
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o ./dist/mediatool ./cmd/mediatool

lint:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

fmt:
	go fmt ./...

release-check:
	goreleaser check
