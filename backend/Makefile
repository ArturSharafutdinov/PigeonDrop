# Developer commands. Run from backend/.
GO ?= go
TOOL = $(GO) tool -modfile=tools/go.mod

.PHONY: generate build test vet fmt lint vuln check db-up db-down run

generate: ## Regenerate code from api/openapi.yaml (oapi-codegen) and internal/db/queries (sqlc)
	$(TOOL) oapi-codegen -config oapi-codegen.yaml api/openapi.yaml
	$(TOOL) sqlc generate

build:
	$(GO) build -o bin/pigeondrop ./cmd/pigeondrop

test: ## Needs PostgreSQL: scripts/dev-db.sh up, or set TEST_DATABASE_URL
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

# Versions of the tools CI runs, so that a local run says the same. golangci-lint is compiled on first use (slow once).
GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION ?= v1.9.0

lint: ## golangci-lint with .golangci.yml
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

vuln: ## Vulnerabilities that this code can actually reach (standard library and modules)
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

check: ## What CI runs, minus the database-backed part being optional locally
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	$(GO) vet ./...
	$(GO) test -race ./...
	@# CI regenerates the code and fails if that changes a file; the same here, without depending on git's state.
	@before=$$(find internal/api internal/db/dbgen -type f | sort | xargs cat | shasum); \
	$(MAKE) --no-print-directory generate; \
	after=$$(find internal/api internal/db/dbgen -type f | sort | xargs cat | shasum); \
	[ "$$before" = "$$after" ] || { echo "generated code was stale: it is regenerated now, review and commit it"; exit 1; }

db-up:
	scripts/dev-db.sh up

db-down:
	scripts/dev-db.sh down

run: build ## Starts the server against the dev database
	DATABASE_URL="$$(scripts/dev-db.sh url)" MIGRATE_ON_START=true STORAGE_DIR=.dev/storage COOKIE_SECURE=false ./bin/pigeondrop serve
