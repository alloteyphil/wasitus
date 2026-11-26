# Run the same checks as CI before pushing:  make check
#
# Every pull request must pass `make check` locally first (see CLAUDE.md).
# CI runs the same steps; this file and .github/workflows/ci.yml must agree.

COVERAGE_FLOOR := 85
GO_TEST := go test -race -shuffle=on -count=1

.PHONY: check fmt tidy vet test cover demo lint

check: fmt tidy vet cover demo lint
	@echo "make check: all passed"

fmt:
	@test -z "$$(gofmt -l .)" || (echo "Not formatted (run: gofmt -w .):"; gofmt -l .; exit 1)

tidy:
	go mod tidy -diff
	cd demo && go mod tidy -diff

vet:
	go vet ./...
	cd demo && go vet ./...

test:
	$(GO_TEST) -coverprofile=coverage.out ./...

cover: test
	scripts/coverage-floor.sh coverage.out $(COVERAGE_FLOOR)

demo:
	cd demo && $(GO_TEST) -timeout 2m ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./... && (cd demo && golangci-lint run ./...); \
	else \
		echo "golangci-lint is not installed (brew install golangci-lint); CI will run it."; \
	fi
