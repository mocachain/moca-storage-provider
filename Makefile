SHELL := /bin/bash

# Load .env file if it exists
-include .env
export

# Configure git to use HTTPS+Token for private repositories if GITHUB_TOKEN is set
ifdef GITHUB_TOKEN
  $(shell git config --global url."https://$(GITHUB_TOKEN):@github.com/".insteadOf "https://github.com/" 2>/dev/null)
endif

.PHONY: all build clean check-go-env check-lint format hooks install-go-test-coverage install-lint install-tools generate lint lint-changed lint-fix lint-staged mock-gen pre-commit pre-commit-staged test test-local test-changed test-staged tidy vet buf-gen proto-clean
.PHONY: install-go-test-coverage check-coverage

GO ?= $(shell for candidate in /opt/homebrew/bin/go /usr/local/go/bin/go "$$(command -v go 2>/dev/null)"; do \
	[ -n "$$candidate" ] || continue; \
	[ -x "$$candidate" ] || continue; \
	if "$$candidate" version 2>/dev/null | awk '{v=substr($$3,3); split(v, a, "."); exit !((a[1] > 1) || (a[1] == 1 && a[2] >= 21))}'; then \
		echo "$$candidate"; \
		break; \
	fi; \
done)
ifeq ($(strip $(GO)),)
GO := go
endif

LEFTHOOK_VERSION ?= v1.11.3
GOLANGCI_LINT_VERSION ?= v1.64.8
GO_TOOLCHAIN ?= $(shell awk '/^toolchain / { print $$2; exit }' go.mod)
GOTOOLCHAIN ?= $(GO_TOOLCHAIN)
export GOTOOLCHAIN
GO_LOCAL_ENV = env -u GOROOT GOTOOLCHAIN=local
GO_REPO_ENV = env -u GOROOT GOTOOLCHAIN=$(GOTOOLCHAIN)
GO_GOBIN := $(shell $(GO_LOCAL_ENV) $(GO) env GOBIN 2>/dev/null)
GO_GOPATH := $(shell $(GO_LOCAL_ENV) $(GO) env GOPATH 2>/dev/null)

ifeq ($(GO_GOBIN),)
golangci_lint_cmd=$(GO_GOPATH)/bin/golangci-lint
lefthook_cmd=$(GO_GOPATH)/bin/lefthook
else
golangci_lint_cmd=$(GO_GOBIN)/golangci-lint
lefthook_cmd=$(GO_GOBIN)/lefthook
endif

help:
	@echo "Please use \`make <target>\` where <target> is one of"
	@echo "  build                 to create build directory and compile sp"
	@echo "  clean                 to remove build directory"
	@echo "  check-go-env          to show the Go binary and toolchain this repo will use"
	@echo "  check-lint            to show the golangci-lint binary this repo will use"
	@echo "  format                to format sp code"
	@echo "  generate              to generate mock code"
	@echo "  hooks                 to install git hooks via lefthook"
	@echo "  install-lint          to install the pinned golangci-lint version"
	@echo "  install-tools         to install mockgen, buf and protoc-gen-gocosmos tools"
	@echo "  lint                  to run golangci lint"
	@echo "  lint-changed          to run golangci-lint on local changed Go files"
	@echo "  lint-fix              to run golangci lint with auto-fixes"
	@echo "  lint-staged           to run golangci-lint only on staged Go files"
	@echo "  mock-gen              to generate mock files"
	@echo "  pre-commit            to run checks for local changed files"
	@echo "  pre-commit-staged     to run checks for staged files (used by git hook)"
	@echo "  test                  to run all sp unit tests"
	@echo "  test-changed          to run unit tests for packages touched by local changed Go files"
	@echo "  test-local            to run local unit tests without coverage output"
	@echo "  test-staged           to run unit tests for packages touched by staged Go files"
	@echo "  tidy                  to run go mod tidy and verify"
	@echo "  vet                   to do static check"
	@echo "  buf-gen               to use buf to generate pb.go files"
	@echo "  proto-clean           to remove generated pb.go files"
	@echo "  proto-format          to format proto files"
	@echo "  proto-format-check    to check proto files"

build:
	bash +x ./build.sh

check-coverage:
	@go-test-coverage --config=./.testcoverage.yml || true

check-go-env:
	@echo "--> Using Go binary: $(GO)"
	@$(GO) version
	@echo "--> Repository toolchain: $(GO_TOOLCHAIN)"
	@echo "--> Ignoring external GOROOT for repository commands"
	@echo "--> Verifying repository toolchain availability (first run may download it)..."
	@$(GO_REPO_ENV) $(GO) env GOVERSION >/dev/null

check-lint:
	@echo "--> Using golangci-lint binary: $(golangci_lint_cmd)"
	@if [ ! -x "$(golangci_lint_cmd)" ]; then \
		echo "golangci-lint is not installed at $(golangci_lint_cmd)"; \
		echo "Run 'make install-lint' first."; \
		exit 1; \
	fi
	@$(golangci_lint_cmd) version

clean:
	rm -rf ./build

format:
	bash script/format.sh
	gofmt -w -l .

generate:
	$(GO_REPO_ENV) $(GO) generate ./...

hooks:
	$(GO_LOCAL_ENV) $(GO) install github.com/evilmartians/lefthook@$(LEFTHOOK_VERSION)
	$(lefthook_cmd) install

install-go-test-coverage:
	$(GO_LOCAL_ENV) $(GO) install github.com/vladopajic/go-test-coverage/v2@latest

install-lint:
	$(GO_LOCAL_ENV) $(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

install-tools:
	$(GO_LOCAL_ENV) $(GO) install go.uber.org/mock/mockgen@v0.1.0
	$(GO_LOCAL_ENV) $(GO) install github.com/bufbuild/buf/cmd/buf@v1.28.0
	$(GO_LOCAL_ENV) $(GO) install github.com/cosmos/gogoproto/protoc-gen-gocosmos@latest

lint: check-go-env check-lint
	@echo "--> Running golangci-lint (first run may download modules and take a few minutes)..."
	@$(GO_REPO_ENV) $(golangci_lint_cmd) run -v

lint-fix: check-go-env check-lint
	@echo "--> Running golangci-lint with fixes (first run may download modules and take a few minutes)..."
	@$(GO_REPO_ENV) $(golangci_lint_cmd) run -v --fix

lint-changed: check-go-env check-lint
	@changed_files="$$( { git diff --name-only --diff-filter=ACMR HEAD; git ls-files --others --exclude-standard; } | grep '\.go$$' | sort -u || true )"; \
	if { git diff --name-only --diff-filter=ACMR HEAD; git ls-files --others --exclude-standard; } | grep -Eq '(^|/)(go\.mod|go\.sum)$$'; then \
		echo "--> go.mod/go.sum changed; running full golangci-lint..."; \
		$(GO_REPO_ENV) $(golangci_lint_cmd) run -v; \
	elif [ -z "$$changed_files" ]; then \
		echo "--> No local changed Go files to lint"; \
	else \
		echo "--> Running golangci-lint on local changed Go files..."; \
		$(GO_REPO_ENV) $(golangci_lint_cmd) run -v $$changed_files; \
	fi

lint-staged: check-go-env check-lint
	@staged_files="$$(git diff --cached --name-only --diff-filter=ACMR | grep '\.go$$' || true)"; \
	if git diff --cached --name-only --diff-filter=ACMR | grep -Eq '(^|/)(go\.mod|go\.sum)$$'; then \
		echo "--> go.mod/go.sum changed; running full golangci-lint..."; \
		$(GO_REPO_ENV) $(golangci_lint_cmd) run -v; \
	elif [ -z "$$staged_files" ]; then \
		echo "--> No staged Go files to lint"; \
	else \
		echo "--> Running golangci-lint on staged Go files..."; \
		$(GO_REPO_ENV) $(golangci_lint_cmd) run -v $$staged_files; \
	fi

pre-commit: lint-changed test-changed

pre-commit-staged: lint-staged test-staged

mock-gen:
	mockgen -source=core/spdb/spdb.go -destination=core/spdb/spdb_mock.go -package=spdb
	mockgen -source=store/bsdb/database.go -destination=store/bsdb/database_mock.go -package=bsdb
	mockgen -source=core/task/task.go -destination=core/task/task_mock.go -package=task

# only run unit tests, exclude e2e tests
test: check-go-env
	@echo "--> Running local unit tests with coverage..."
	@pkgs="$$($(GO_REPO_ENV) $(GO) list ./... | grep -v e2e | grep -v modular/blocksyncer)"; \
	$(GO_REPO_ENV) $(GO) test -failfast $$pkgs -covermode=atomic -coverprofile=./coverage.out -timeout 99999s
	# go test -cover ./...
	# go test -coverprofile=coverage.out ./...
	# go tool cover -html=coverage.out

# Run the same local-only unit test set as `test` without writing coverage
# artifacts, so pre-commit checks do not modify the worktree.
test-local: check-go-env
	@echo "--> Running local unit tests..."
	@pkgs="$$($(GO_REPO_ENV) $(GO) list ./... | grep -v e2e | grep -v modular/blocksyncer)"; \
	$(GO_REPO_ENV) $(GO) test -failfast $$pkgs -timeout 99999s

test-changed: check-go-env
	@changed_dirs="$$( { git diff --name-only --diff-filter=ACMR HEAD; git ls-files --others --exclude-standard; } | grep '\.go$$' | xargs -n1 dirname 2>/dev/null | sed 's#^\.$$#./#' | sort -u || true )"; \
	if { git diff --name-only --diff-filter=ACMR HEAD; git ls-files --others --exclude-standard; } | grep -Eq '(^|/)(go\.mod|go\.sum)$$'; then \
		echo "--> go.mod/go.sum changed; running full local unit tests..."; \
		pkgs="$$($(GO_REPO_ENV) $(GO) list ./... | grep -v e2e | grep -v modular/blocksyncer)"; \
		$(GO_REPO_ENV) $(GO) test -failfast $$pkgs -timeout 99999s; \
	elif [ -z "$$changed_dirs" ]; then \
		echo "--> No local changed Go packages to test"; \
	else \
		echo "--> Running unit tests for local changed Go packages..."; \
		pkgs="$$(printf '%s\n' $$changed_dirs | xargs $(GO_REPO_ENV) $(GO) list 2>/dev/null | grep -v e2e | grep -v modular/blocksyncer | sort -u)"; \
		if [ -z "$$pkgs" ]; then \
			echo "--> No testable Go packages matched the local changed files"; \
		else \
			$(GO_REPO_ENV) $(GO) test -failfast $$pkgs -timeout 99999s; \
		fi; \
	fi

test-staged: check-go-env
	@staged_dirs="$$(git diff --cached --name-only --diff-filter=ACMR | grep '\.go$$' | xargs -n1 dirname 2>/dev/null | sed 's#^\.$$#./#' | sort -u || true)"; \
	if git diff --cached --name-only --diff-filter=ACMR | grep -Eq '(^|/)(go\.mod|go\.sum)$$'; then \
		echo "--> go.mod/go.sum changed; running full local unit tests..."; \
		pkgs="$$($(GO_REPO_ENV) $(GO) list ./... | grep -v e2e | grep -v modular/blocksyncer)"; \
		$(GO_REPO_ENV) $(GO) test -failfast $$pkgs -timeout 99999s; \
	elif [ -z "$$staged_dirs" ]; then \
		echo "--> No staged Go packages to test"; \
	else \
		echo "--> Running unit tests for staged Go packages..."; \
		pkgs="$$(printf '%s\n' $$staged_dirs | xargs $(GO_REPO_ENV) $(GO) list 2>/dev/null | grep -v e2e | grep -v modular/blocksyncer | sort -u)"; \
		if [ -z "$$pkgs" ]; then \
			echo "--> No testable Go packages matched the staged files"; \
		else \
			$(GO_REPO_ENV) $(GO) test -failfast $$pkgs -timeout 99999s; \
		fi; \
	fi

tidy:
	$(GO_REPO_ENV) $(GO) mod tidy
	$(GO_REPO_ENV) $(GO) mod verify

vet:
	$(GO_REPO_ENV) $(GO) vet ./...

buf-gen:
	rm -rf ./base/types/*/*.pb.go && rm -rf ./modular/metadata/types/*.pb.go && rm -rf ./store/types/*.pb.go
	buf generate

proto-clean:
	rm -rf ./base/types/*/*.pb.go && rm -rf ./modular/metadata/types/*.pb.go && rm -rf ./store/types/*.pb.go

proto-format:
	buf format -w

proto-format-check:
	buf format --diff --exit-code

###############################################################################
###                        Docker                                           ###
###############################################################################
DOCKER := $(shell which docker)
DOCKER_IMAGE := mocachain/moca-storage-provider
COMMIT_HASH := $(shell git rev-parse --short=7 HEAD)
DOCKER_TAG := $(COMMIT_HASH)

build-docker:
	$(DOCKER) build -t ${DOCKER_IMAGE}:${DOCKER_TAG} .
	$(DOCKER) tag ${DOCKER_IMAGE}:${DOCKER_TAG} ${DOCKER_IMAGE}:latest
	$(DOCKER) tag ${DOCKER_IMAGE}:${DOCKER_TAG} ${DOCKER_IMAGE}:${COMMIT_HASH}

.PHONY: build-docker
###############################################################################
###                        Docker Compose                                   ###
###############################################################################
build-dcf:
	go run cmd/ci/main.go

start-dc:
	docker compose up -d
	docker compose ps

stop-dc:
	docker compose down --volumes

.PHONY: build-dcf start-dc stop-dc

###############################################################################
###                                Releasing                                ###
###############################################################################

PACKAGE_NAME:=github.com/mocachain/moca-storage-provider
GOLANG_CROSS_VERSION  = v1.23
GOPATH ?= $(HOME)/go
release-dry-run:
	docker run \
		--rm \
		--privileged \
		-e CGO_ENABLED=1 \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v `pwd`:/go/src/$(PACKAGE_NAME) \
		-v ${GOPATH}/pkg:/go/pkg \
		-w /go/src/$(PACKAGE_NAME) \
		ghcr.io/goreleaser/goreleaser-cross:${GOLANG_CROSS_VERSION} \
		--clean --skip validate --skip publish --skip docker --snapshot

release:
	@if [ ! -f ".release-env" ]; then \
		echo "\033[91m.release-env is required for release\033[0m";\
		exit 1;\
	fi
	docker run \
		--rm \
		--privileged \
		-e CGO_ENABLED=1 \
		--env-file .release-env \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v `pwd`:/go/src/$(PACKAGE_NAME) \
		-w /go/src/$(PACKAGE_NAME) \
		ghcr.io/goreleaser/goreleaser-cross:${GOLANG_CROSS_VERSION} \
		release --clean --skip validate

.PHONY: release-dry-run release
