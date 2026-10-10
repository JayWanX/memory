# Memory build and verification entry points.

VERSION ?= dev
GOBIN   := $(shell go env GOBIN)
ifeq ($(GOBIN),)
  GOBIN := $(shell go env GOPATH)/bin
endif

BIN_DIR        := bin
MEMORY         := $(BIN_DIR)/memory
MEMORY_LDFLAGS := -s -w -X github.com/JayWanX/memory/cmd.version=$(VERSION)

# Regular CI deliberately excludes real daemon readiness, process, TCP, Docker,
# JavaScript runtime, and paid-provider tests. Those belong to the explicit
# integration and live tiers below.
DETERMINISTIC_PKGS := \
	. \
	./cmd \
	./cmd/agency \
	./cmd/memory \
	./internal/agency \
	./internal/agency/client \
	./internal/agency/attach \
	./internal/agency/authority \
	./internal/agency/artifact \
	./internal/memory/embed \
	./internal/memory/graph \
	./internal/memory/importdraft \
	./internal/memory/internal/topk \
	./internal/memory/model \
	./internal/memory/search \
	./internal/memory/setup \
	./internal/memory/setup/assets \
	./internal/memory/store \
	./test/memoryd/architecture \
	./test/memoryd/observer \
	./test/memoryd/domainops/trace

TESTDATA_PKGS := \
	./testdata/memoryd/domainops/cmd/domain-load \
	./testdata/memoryd/domainops/cmd/domain-world \
	./testdata/memoryd/domainops/cmd/domainctl \
	./testdata/memoryd/domainops/world

.PHONY: deps build install uninstall test test-integration test-live
.PHONY: docker-build docker-run compose-up compose-down compose-dev release-snapshot clean help

.DEFAULT_GOAL := help

deps: ## Download Go dependencies
	go mod download

build: ## Build the memory product binary
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(MEMORY_LDFLAGS)" -o $(MEMORY) .

install: build ## Install memory to GOBIN
	@mkdir -p $(GOBIN)
	cp $(MEMORY) $(GOBIN)/memory
	@echo "Installed: $(GOBIN)/memory"

uninstall: ## Remove memory from GOBIN
	rm -f $(GOBIN)/memory
	@echo "Removed: $(GOBIN)/memory"
	@echo "Run 'memory setup --eject' first to remove Memory integrations."

test: ## Run the deterministic CI suite
	go vet ./...
	go test $(DETERMINISTIC_PKGS) -count=1

test-integration: ## Run opt-in E2E, process, network, race, runtime, and Docker tests
	bash scripts/e2e_test.sh
	go test -p 1 ./... $(TESTDATA_PKGS) -count=1
	go test -race -p 1 ./internal/... ./cmd/agency $(TESTDATA_PKGS) -count=1
	test/memoryd/scenarios/run_cases.sh
	test/memoryd/runtime/pi/run_delegate_oracle.sh
	test/memoryd/domainops/run_world.sh

test-live: ## Run the paid Pi/DeepSeek smoke and federated operations case
	@test -n "$${DEEPSEEK_API_KEY:-}" || { echo "error: DEEPSEEK_API_KEY is required" >&2; exit 2; }
	LIVE_PI=1 test/memoryd/scenarios/run_live_pi.sh
	LIVE_DOMAIN_OPS=1 test/memoryd/domainops/run_live.sh

docker-build: ## Build the runtime Docker image
	docker build --target runtime --build-arg VERSION=$(VERSION) -t JayWanX/memory:$(VERSION) .

docker-run: ## Run memory status in Docker with local .env
	docker run --rm --env-file .env -v memory-data:/memory JayWanX/memory:$(VERSION) status

compose-up: ## Start the Memory container
	docker compose up -d memory

compose-down: ## Stop Docker Compose services
	docker compose down

compose-dev: ## Open a development shell in Docker Compose
	docker compose --profile dev run --rm JayWanX

release-snapshot: ## Build local GoReleaser snapshot artifacts
	goreleaser release --snapshot --clean

clean: ## Remove build artifacts and test data
	rm -f memory
	rm -f $(BIN_DIR)/memory
	rm -rf .testdata

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
