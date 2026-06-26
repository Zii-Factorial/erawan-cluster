-include /etc/erawan-cluster/.env
-include .envrc
export

%:
	@:

.PHONY: run
run:
	@go run ./cmd/api

.PHONY: build
build:
	@mkdir -p bin
	@go build -o bin/erawan-cluster ./cmd/api

.PHONY: test
test:
	@go test ./...

# Run only the black-box unit suite under tests/unit.
.PHONY: test-unit
test-unit:
	@go test ./tests/unit/...

# Unit suite with coverage attributed to the production packages it exercises.
.PHONY: cover
cover:
	@go test -coverpkg=./internal/...,./cmd/... ./tests/unit/...

.PHONY: fmt
fmt:
	@gofmt -w cmd internal

.PHONY: fmtcheck
fmtcheck:
	@test -z "$$(gofmt -l cmd internal)" || { echo "unformatted files:"; gofmt -l cmd internal; exit 1; }

.PHONY: vet
vet:
	@go vet ./...

.PHONY: staticcheck
staticcheck:
	@go run honnef.co/go/tools/cmd/staticcheck@latest ./...

.PHONY: vulncheck
vulncheck:
	@go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: tidy
tidy:
	@go mod tidy

# Database migrations.
#   make migration                  — apply all pending migrations
#   make migration TABLE=<name>     — scaffold a new migration pair
#   make migration ROLLBACK=<name>  — roll back the named migration
.PHONY: migration
migration:
ifdef TABLE
	@bash scripts/migrate.sh create $(TABLE)
else ifdef ROLLBACK
	@bash scripts/migrate.sh rollback $(ROLLBACK)
else
	@bash scripts/migrate.sh up
endif

# Aggregate quality gate: run before every commit / in CI.
.PHONY: check
check: fmtcheck vet staticcheck vulncheck test
