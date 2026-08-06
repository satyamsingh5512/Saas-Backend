.PHONY: run build test test-verbose vet fmt tidy db-up db-down clean migrate-up migrate-down migrate-version db-provision-app-role check-web build-vercel

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

## Syntax-check the dashboard scripts. Not covered by `go vet`, so a typo in
## app.js would otherwise only surface in a browser.
check-web:
	node --check internal/routes/web/assets/app.js
	node --check internal/routes/web/assets/landing.js
	node --check internal/routes/web/assets/theme.js

## Assemble the Vercel static output locally, exactly as Vercel does at deploy
## time. Useful for inspecting the landing/dashboard document swap before pushing.
build-vercel:
	sh scripts/build_vercel_static.sh build/web

## Remove build artifacts
clean:
	rm -rf bin/ build/
