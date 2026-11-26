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

# ---- Demo lab (needs Docker; see demo/README.md) ----------------------------

LAB := docker compose -f demo/compose.yaml

.PHONY: lab-up lab-down lab-reset lab-ps lab-logs lab-smoke

# Build and start SigNoz and the lab, wait until every service is healthy,
# then finish SigNoz's first-run setup (it ingests nothing until then).
lab-up: demo/.env
	$(LAB) up -d --build --wait --wait-timeout 300
	demo/signoz/setup.sh
	@echo "Lab is up: storefront http://localhost:8000  mockpay http://localhost:8090  SigNoz http://localhost:8080"

lab-down:
	$(LAB) down

# Stop the lab and delete its data (SigNoz databases included).
lab-reset:
	$(LAB) down --volumes

lab-ps:
	$(LAB) ps

lab-logs:
	$(LAB) logs -f --tail=50

# Prove the running lab works end to end: checkouts, a staged failure, and the
# traces of both in SigNoz. CI runs this too (.github/workflows/lab.yml).
lab-smoke:
	demo/scripts/smoke.sh

# A random local password for SigNoz's Postgres, created once. demo/.env is
# ignored by git, so no password is ever committed.
demo/.env:
	@umask 077 && printf 'LAB_POSTGRES_PASSWORD=%s\n' "$$(openssl rand -hex 16)" > $@
	@echo "Created $@ with a random LAB_POSTGRES_PASSWORD"
