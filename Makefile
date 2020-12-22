APP_VERSION := $(shell git describe --tags --always)
BUILD_DIR := build

GO := GO111MODULE=on go
GOBIN := $(GOPATH)/bin
RACEFLAG ?= -race

define exit_on_output
	@OUTPUT=$$(eval $(1)); [ -z "$${OUTPUT}" ] || (echo "$${OUTPUT}"; false)
endef

.PHONY: \
	all fix check \
	fiximports fixformat \
	vet lint imports format staticcheck test \
	build build-rpi

all: dep fix check build
fix: fiximports fixformat
check: vet lint imports format staticcheck test

dep:
	@echo ">> installing dependencies"
	@$(GO) install github.com/jstemmer/go-junit-report
	@$(GO) install golang.org/x/lint/golint
	@$(GO) install golang.org/x/tools/cmd/goimports
	@$(GO) install honnef.co/go/tools/cmd/staticcheck
	@$(GO) get -t -d ./...

vet:
	@echo ">> checking go vet"
	@$(GO) vet ./...

lint:
	@echo ">> checking golint"
	$(call exit_on_output, "$(GOBIN)/golint ./... | grep -v 'should have comment or be unexported'")

fiximports:
	@echo ">> fixing goimports"
	@$(GOBIN)/goimports -w .

imports:
	@echo ">> checking goimports"
	$(call exit_on_output, "$(GOBIN)/goimports -l -d .")

fixformat:
	@echo ">> fixing gofmt"
	@gofmt -w .

format:
	@echo ">> checking gofmt"
	$(call exit_on_output, "gofmt -l -d .")

staticcheck:
	@echo ">> checking staticcheck"
	@$(GOBIN)/staticcheck ./... || exit 1

test:
	@echo ">> running tests"
	@$(GO) test $(RACEFLAG) -v ./... -coverprofile coverage.txt

build:
	@echo ">> building"
	@$(GO) build -a -tags 'netgo static_build' -ldflags="\
		-X main.version=$(APP_VERSION) "\
		-o $(BUILD_DIR) ./...
	@cp README.md $(BUILD_DIR)/

build-rpi3:
	@echo ">> building for rpi"
	@export GOOS=linux GOARCH=arm GOARM=7 && $(MAKE) -s build

build-win:
	@echo ">> building for windows"
	@export GOOS=windows GOARCH=amd64 && $(MAKE) -s build
