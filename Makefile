# Run CI's checks on your machine before pushing:  make check
#
# Every pull request must pass `make check` locally first (see CLAUDE.md).
# It covers formatting, tidy, vet, build, tests (same flags as CI), the
# coverage floor and the lab, plus golangci-lint when installed. CI also runs
# the vulnerability scan, workflow lint and the release dry run. Keep the test
# flags and the coverage floor in step with .github/workflows/ci.yml.

COVERAGE_FLOOR := 85
GO_TEST := go test -race -shuffle=on -count=1

.PHONY: check fmt tidy vet build test cover demo lint

check: fmt tidy vet build cover demo lint
	@echo "make check: all passed"

fmt:
	@unformatted="$$(gofmt -l .)" && if [ -n "$$unformatted" ]; then \
		echo "Not formatted (run: gofmt -w .):"; echo "$$unformatted"; exit 1; fi

build:
	go build -o /dev/null ./cmd/wasitus

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
