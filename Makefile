.PHONY: build install clean test fmt lint

VERSION := 0.1.0
BINARY := ccvalet
BUILD_DIR := bin

build:
	go build -o $(BUILD_DIR)/$(BINARY) ./cmd/ccvalet

install:
	go install ./cmd/ccvalet

clean:
	rm -rf $(BUILD_DIR)

test:
	go test -v ./...

fmt:
	go fmt ./...

lint:
	go vet ./...
