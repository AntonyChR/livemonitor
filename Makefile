.PHONY: all build run test test-race vet clean

COMMIT_HASH := $(shell git log --oneline | head -n 1 | awk '{print $$1}')

BINARY_NAME=bin/livemonitor

all: build

build:
	@mkdir -p bin
	go build -ldflags="-X 'main.commitHash=$(COMMIT_HASH)'" -o $(BINARY_NAME) ./cmd/recorder

install: build
	cp ./$(BINARY_NAME) ~/apps/monitor

run: build
	./$(BINARY_NAME) -users ./user_list.txt

test:
	go test -v ./...

test-race:
	go test -v -race ./...

vet:
	go vet ./...

clean:
	rm -rf bin/
