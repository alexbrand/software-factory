# Software Factory - Agent Orchestration Platform
# This Makefile provides the build/test/lint/generate loop that agents
# use to verify their work. Run `make all` to check everything.

# Tool versions
CONTROLLER_GEN_VERSION ?= v0.17.2
GOLANGCI_LINT_VERSION ?= v1.64.8

# Binaries
CONTROLLER_GEN ?= $(shell which controller-gen 2>/dev/null)
GOLANGCI_LINT ?= $(shell which golangci-lint 2>/dev/null)

# Go settings
GOBIN ?= $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

##@ General

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: all
all: generate build lint test ## Run the full verification loop

##@ Development

.PHONY: generate
generate: controller-gen ## Generate CRD manifests, RBAC, and deepcopy
	$(CONTROLLER_GEN) object paths="./api/..." || true
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases || true
	$(CONTROLLER_GEN) rbac:roleName=controller-manager-role paths="./internal/controller/..." output:rbac:dir=config/rbac || true

.PHONY: fmt
fmt: ## Run go fmt
	go fmt ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

##@ Build

.PHONY: build
build: ## Build all binaries
	go build -o bin/controller-manager ./cmd/controller-manager
	go build -o bin/apiserver ./cmd/apiserver
	go build -o bin/bridge ./cmd/bridge

##@ Test

.PHONY: test
test: ## Run unit tests
	go test ./... -race

.PHONY: test-verbose
test-verbose: ## Run unit tests with verbose output
	go test ./... -race -v -coverprofile=coverage.out

##@ Lint

.PHONY: lint
lint: golangci-lint ## Run golangci-lint
	$(GOLANGCI_LINT) run --timeout 5m

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint with auto-fix
	$(GOLANGCI_LINT) run --timeout 5m --fix

##@ CRD

.PHONY: manifests
manifests: controller-gen ## Generate CRD YAML into config/crd/bases/
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases

##@ Docker

.PHONY: docker-build
docker-build: ## Build Docker images for all binaries
	docker build -t software-factory-controller-manager:latest --target controller-manager .
	docker build -t software-factory-apiserver:latest --target apiserver .
	docker build -t software-factory-bridge:latest --target bridge .

##@ Tools

.PHONY: controller-gen
controller-gen: ## Install controller-gen if not present
ifeq (,$(CONTROLLER_GEN))
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)
	$(eval CONTROLLER_GEN := $(GOBIN)/controller-gen)
endif

.PHONY: golangci-lint
golangci-lint: ## Install golangci-lint if not present
ifeq (,$(GOLANGCI_LINT))
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(eval GOLANGCI_LINT := $(GOBIN)/golangci-lint)
endif

##@ Clean

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin/ coverage.out
