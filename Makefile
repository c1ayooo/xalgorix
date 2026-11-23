.PHONY: build run clean test test-ci test-cover test-race install fmt vet lint tidy all webui webui-install webui-dev toolbox-build toolbox-verify toolbox-selftest toolbox-shim image-platform image-toolbox image-runnerd deploy-check

BINARY=xalgorix
BUILD_DIR=./build
VERSION=4.6.133
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"

webui/node_modules: webui/package.json webui/package-lock.json
	@echo "Installing webui dependencies (locked)..."
	cd webui && npm ci --no-audit --no-fund
	@touch webui/node_modules

webui-install: webui/node_modules

webui: webui/node_modules
	@echo "Building webui (React) → internal/web/static..."
	cd webui && npm run build

webui-dev: webui/node_modules
	cd webui && npm run dev

build: webui
	@echo "Building $(BINARY)..."
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/xalgorix/
	@echo "Built: $(BUILD_DIR)/$(BINARY)"

run:
	$(if $(filter docker,$(RUNTIME_MODE)),XALGORIX_RUNTIME_BACKEND=docker ,)go run ./cmd/xalgorix/ $(ARGS)

clean:
	rm -rf $(BUILD_DIR)
	go clean

test:
	go test ./... -v

test-cover:
	go test ./... -cover

test-race:
	go test ./... -race

test-ci:
	go test ./...
	go test ./... -cover
	go test ./... -race
	go vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; else echo "staticcheck not installed; skipping"; fi
	go build ./cmd/xalgorix

install: build
	@echo "Installing $(BINARY) to /usr/local/bin..."
	sudo cp $(BUILD_DIR)/$(BINARY) /usr/local/bin/$(BINARY)
	sudo chmod +x /usr/local/bin/$(BINARY)
	@echo "Installed!"

fmt:
	go fmt ./...

vet:
	go vet ./...

# Keep in sync with GOLANGCI_LINT_VERSION in .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION ?= v2.12.2

lint: fmt vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo "Running golangci-lint..."; \
		golangci-lint run ./... || exit 1; \
	else \
		echo "golangci-lint not found — skipping (CI enforces it)."; \
		echo "Install the pinned version with:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)"; \
	fi
	@echo "Lint passed"

tidy:
	go mod tidy

# ── Build / deploy variant selectors ─────────────────────────────────────────
# Topology is a build/deploy-time choice, NEVER a Go build tag: every image
# ships the same xalgorix binary, and the container it runs in decides whether
# tools execute natively or in per-project toolbox containers.
#
#   PLATFORM_VARIANT  slim      control-plane only (no security tooling)
#                     allinone  batteries-included Kali image (default)
#   RUNTIME_MODE      native    tools run in the platform container (default)
#                     docker    tools run in per-project toolbox containers
#   TOOLBOX_PROFILE   core      baseline recon/HTTP web toolset (default)
#                     full      core + exploitation/SAST tooling
#                     web       core + browser/DAST tooling
PLATFORM_VARIANT ?= allinone
RUNTIME_MODE     ?= native
TOOLBOX_PROFILE  ?= core

PLATFORM_IMAGE ?= xalgorix:$(VERSION)
TOOLBOX_IMAGE  ?= xalgorix-toolbox:dev
RUNNERD_IMAGE  ?= xalgorix-runnerd:dev
# Module proxy used inside the image builds. Override on networks where the
# default proxy is unreachable, e.g. GOPROXY=https://goproxy.cn,direct.
GOPROXY ?= https://proxy.golang.org,direct
# Kali apt mirror. USTC is the default for the build environment; any Kali
# mirror can be selected.
KALI_MIRROR ?= http://mirrors.ustc.edu.cn/kali
DOCKER_BUILD_ARGS ?= --build-arg GOPROXY=$(GOPROXY) --build-arg KALI_MIRROR=$(KALI_MIRROR)

# Expand to a parse-time error so `make -n <target>` rejects typos too, before
# any Docker command is printed or executed.
define require_one_of
$(if $(filter $(2),$(3)),,$(error $(1)='$(2)' is invalid; expected one of: $(3)))
endef

ifneq (,$(filter image-platform,$(MAKECMDGOALS)))
$(call require_one_of,PLATFORM_VARIANT,$(PLATFORM_VARIANT),slim allinone)
endif
ifneq (,$(filter image-toolbox toolbox-build toolbox-verify toolbox-push toolbox-selftest,$(MAKECMDGOALS)))
$(call require_one_of,TOOLBOX_PROFILE,$(TOOLBOX_PROFILE),core full web)
endif
ifneq (,$(filter run,$(MAKECMDGOALS)))
$(call require_one_of,RUNTIME_MODE,$(RUNTIME_MODE),native docker)
endif

# Platform image: <repo>:<version>-<variant>. The slim variant is the
# control-plane half of the split deployment and is only useful together with
# XALGORIX_RUNTIME_BACKEND=docker.
image-platform:
	@echo "Building platform image $(PLATFORM_VARIANT): $(PLATFORM_IMAGE)-$(PLATFORM_VARIANT)"
	docker build -f Dockerfile --target platform-$(PLATFORM_VARIANT) -t $(PLATFORM_IMAGE)-$(PLATFORM_VARIANT) $(DOCKER_BUILD_ARGS) --build-arg VERSION=$(VERSION) .

# Toolbox image: <toolbox-repo>:<version>-<profile>.
image-toolbox:
	@echo "Building toolbox image $(TOOLBOX_PROFILE): $(TOOLBOX_IMAGE)-$(TOOLBOX_PROFILE)"
	docker build -f tools/toolbox/Dockerfile --target $(TOOLBOX_PROFILE) -t $(TOOLBOX_IMAGE)-$(TOOLBOX_PROFILE) $(DOCKER_BUILD_ARGS) .

# Host-side runtime service used by the recommended split deployment.
image-runnerd:
	@echo "Building runnerd image: $(RUNNERD_IMAGE)"
	docker build -f Dockerfile --target runnerd -t $(RUNNERD_IMAGE) $(DOCKER_BUILD_ARGS) --build-arg VERSION=$(VERSION) .

# Validate both deployment shapes without contacting a Docker daemon: the
# profiles must render, and the default (no profile) must still select services
# so `docker compose up -d` keeps working for existing users.
deploy-check:
	docker compose -f docker-compose.yml --profile allinone config >/dev/null
	docker compose -f docker-compose.yml --profile split config >/dev/null
	docker compose -f deploy/docker-compose.yml --profile allinone config >/dev/null
	docker compose -f deploy/docker-compose.yml --profile split config >/dev/null
	@test -n "$$(docker compose -f docker-compose.yml config --services)" || { \
		echo "deploy-check: docker-compose.yml selects no services without a profile" >&2; exit 1; }
	@test -n "$$(docker compose -f deploy/docker-compose.yml config --services)" || { \
		echo "deploy-check: deploy/docker-compose.yml selects no services without a profile" >&2; exit 1; }
	@echo "deploy-check: compose variants are valid"

# ── Toolbox image (per-project execution plane) ──────────────────────────────
# TOOLBOX_IMAGE / TOOLBOX_PROFILE are declared with the selectors above.

# Build the image target (alias of image-toolbox). Requires a Docker daemon and
# network access; expect tens of minutes on a cold cache and several GB of disk.
toolbox-build: image-toolbox

# Build and run the self-test gate inside the resulting image.
toolbox-verify: toolbox-build
	docker run --rm --cap-add NET_RAW --cap-add NET_ADMIN --cap-add SYS_PTRACE \
		--entrypoint /opt/xalgorix/bin/toolbox-selftest \
		$(TOOLBOX_IMAGE)-$(TOOLBOX_PROFILE) --profile $(TOOLBOX_PROFILE) --json

# Run the self-test against the local host (useful while iterating on the
# manifest; most tools will be missing, which is the point).
toolbox-selftest:
	go run ./tools/toolbox/selftest --profile $(TOOLBOX_PROFILE) --toolpack tools/toolbox/toolpack.yaml

# Build the in-container helpers without Docker.
toolbox-shim:
	go build -trimpath -o $(BUILD_DIR)/toolshim ./cmd/toolshim
	go build -trimpath -o $(BUILD_DIR)/toolbox-selftest ./tools/toolbox/selftest
	go build -trimpath -o $(BUILD_DIR)/runnerd ./cmd/runnerd

TOOLBOX_IMAGE_PUSH ?= ghcr.io/c1ayooo/xalgorix-toolbox

# Push both the semver tag and the digest-pinned reference the platform expects.
toolbox-push:
	@test -n "$(VERSION)" || (echo "VERSION is required" && exit 1)
	docker tag $(TOOLBOX_IMAGE)-$(TOOLBOX_PROFILE) $(TOOLBOX_IMAGE_PUSH):$(VERSION)-$(TOOLBOX_PROFILE)
	docker push $(TOOLBOX_IMAGE_PUSH):$(VERSION)-$(TOOLBOX_PROFILE)
	@docker inspect --format='{{index .RepoDigests 0}}' $(TOOLBOX_IMAGE_PUSH):$(VERSION)-$(TOOLBOX_PROFILE)

all: tidy lint build
