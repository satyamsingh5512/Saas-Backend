.PHONY: run build test test-verbose vet fmt tidy db-up db-down clean migrate-up migrate-down migrate-version db-provision-app-role

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

## Apply all pending SQL migrations
migrate-up:
	go run ./cmd/migrate up

## Revert the most recent SQL migration
migrate-down:
	go run ./cmd/migrate down 1

## Print the current migration version
migrate-version:
	go run ./cmd/migrate version

## Provision the low-privilege app_user Postgres role for local/dev use.
## Requires a superuser connection (the default local docker-compose setup).
db-provision-app-role:
	docker exec -i $$(docker compose ps -q postgres) psql -U postgres -d tenant_saas -f - < scripts/provision_app_role.sql

## Remove build artifacts
clean:
	rm -rf bin/
