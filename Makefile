.PHONY: build install clean run test lint release

VERSION := 0.1.0
BINARY := ccvalet
BUILD_DIR := bin

build:
	go build -o $(BUILD_DIR)/$(BINARY) ./cmd/ccvalet

install:
	go install ./cmd/ccvalet

run: build
	./$(BUILD_DIR)/$(BINARY) run

clean:
	rm -rf $(BUILD_DIR)

test:
	go test -v ./...

lint:
	golangci-lint run

# Cross-compilation
release:
	GOOS=darwin GOARCH=amd64 go build -o dist/$(BINARY)-darwin-amd64 ./cmd/ccvalet
	GOOS=darwin GOARCH=arm64 go build -o dist/$(BINARY)-darwin-arm64 ./cmd/ccvalet
	GOOS=linux GOARCH=amd64 go build -o dist/$(BINARY)-linux-amd64 ./cmd/ccvalet
