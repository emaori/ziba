BINARY  := ziba
PKG     := github.com/emaori/ziba
VERSION ?= $(shell date +%Y%m%d)-dev

.PHONY: build run test fmt vet tidy check clean

## build: compile the binary into bin/
build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BINARY) ./cmd/$(BINARY)

## run: build and run, e.g. make run ARGS=version
run:
	go run ./cmd/$(BINARY) $(ARGS)

## test: run the test suite
test:
	go test ./...

## fmt: format all Go files
fmt:
	go fmt ./...

## vet: report suspicious constructs
vet:
	go vet ./...

## tidy: sync go.mod with the imports actually used
tidy:
	go mod tidy

## check: what should pass before a commit
check: fmt vet test

## clean: remove build output
clean:
	rm -rf bin/
