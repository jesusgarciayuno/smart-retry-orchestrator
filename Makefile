.PHONY: all build test run demo generate-data fmt lint clean

BINARY=smart-retry-orchestrator
PORT?=8080

all: fmt lint test build

build:
	go build -o bin/$(BINARY) ./cmd/server

test:
	go test -race -count=1 -v ./...

run: build
	PORT=$(PORT) ./bin/$(BINARY)

demo: build
	bash scripts/demo.sh

generate-data:
	@curl -s -X POST http://localhost:$(PORT)/api/v1/test/generate | jq .

fmt:
	go fmt ./...

lint:
	go vet ./...

clean:
	rm -rf bin/
