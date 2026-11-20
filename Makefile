.PHONY: all build run test test-race vet clean

BINARY_NAME=bin/recorder

all: build

build:
	@mkdir -p bin
	go build -o $(BINARY_NAME) ./cmd/recorder

install: build
	cp ./$(BINARY_NAME) ~/apps/monitor2

run: build
	./$(BINARY_NAME) -users ./bin/user_list.txt

test:
	go test -v ./...

test-race:
	go test -v -race ./...

vet:
	go vet ./...

clean:
	rm -rf bin/
