.PHONY: all build test clean

BIN_DIR ?= bin
BINS := manager visualizer

all: build

build:
	@mkdir -p $(BIN_DIR)
	@for bin in $(BINS); do \
		go build -o $(BIN_DIR)/$$bin ./cmd/$$bin || exit 1; \
	done

test:
	go test -v -race ./...

clean:
	rm -rf $(BIN_DIR)
