# Developer commands. Run from backend/.
GO ?= go
TOOL = $(GO) tool -modfile=tools/go.mod

.PHONY: generate build test vet fmt check db-up db-down run

generate: ## Regenerate code from api/openapi.yaml (and, once queries exist, sqlc)
	$(TOOL) oapi-codegen -config oapi-codegen.yaml api/openapi.yaml
	@if [ -f sqlc.yaml ]; then $(TOOL) sqlc generate; fi

build:
	$(GO) build -o bin/pigeondrop ./cmd/pigeondrop

test: ## Needs PostgreSQL: scripts/dev-db.sh up, or set TEST_DATABASE_URL
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

check: ## What CI runs, minus the database-backed part being optional locally
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	$(GO) vet ./...
	$(GO) test ./...

db-up:
	scripts/dev-db.sh up

db-down:
	scripts/dev-db.sh down

run: build ## Starts the server against the dev database
	DATABASE_URL="$$(scripts/dev-db.sh url)" MIGRATE_ON_START=true STORAGE_DIR=.dev/storage ./bin/pigeondrop serve
