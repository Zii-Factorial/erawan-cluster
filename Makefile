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

# Aggregate quality gate: run before every commit / in CI.
.PHONY: check
check: fmtcheck vet staticcheck vulncheck test
