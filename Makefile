.PHONY: all download test build

all: install test build

install:
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go mod download

run:
	go run .

test:
	go fmt ./...
	go mod tidy -v
	go mod verify
	go vet ./...
	staticcheck -checks=all ./...
	go test ./...

build:
	mkdir -p ./dist
	go build -o ./dist/ ./cmd/ac3converter
	go build -o ./dist/ ./cmd/dupfinder
