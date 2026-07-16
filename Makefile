.PHONY: run build test test-verbose vet fmt tidy db-up db-down clean

## Run the server locally (requires DB running and .env configured)
run:
	go run ./cmd/server

## Build the server binary into ./bin/server
build:
	go build -o bin/server ./cmd/server

## Run all tests
test:
	go test ./...

## Run all tests with verbose output
test-verbose:
	go test ./... -v

## Run go vet
vet:
	go vet ./...

## Format all Go files
fmt:
	gofmt -w .

## Tidy go.mod/go.sum
tidy:
	go mod tidy

## Start local Postgres via docker compose
db-up:
	docker compose up -d

## Stop local Postgres
db-down:
	docker compose down

## Remove build artifacts
clean:
	rm -rf bin/
