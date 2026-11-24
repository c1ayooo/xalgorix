.PHONY: build run clean test test-ci test-cover test-race install fmt vet lint tidy all webui webui-install webui-dev toolbox-build toolbox-verify toolbox-selftest toolbox-shim image-platform image-toolbox image-runnerd deploy-check stack-plan stack-build stack-run

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
# STACK_MODE selects the deployment shape:
#   native  — platform host binary + tools in-process (no toolbox image)
#   hybrid  — platform host binary + per-project toolbox containers
#   split   — slim platform container + runnerd + per-project toolbox containers
STACK_MODE ?= native
# PLATFORM_PACKAGING selects how the platform image is produced:
#   native  — host build outputs build/xalgorix; Docker only COPYs it
#   docker  — Docker compiles webui+Go inside the image (CI/reproducible)
PLATFORM_PACKAGING ?= native
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

ifneq (,$(filter stack-plan stack-build stack-run,$(MAKECMDGOALS)))
$(call require_one_of,STACK_MODE,$(STACK_MODE),native hybrid split)
endif
ifeq ($(STACK_MODE),split)
# Split always runs the control-plane-only image; override the historical
# allinone default. A command-line PLATFORM_PACKAGING still wins.
PLATFORM_VARIANT := slim
PLATFORM_PACKAGING := native
endif
# An all-in-one image necessarily compiles the Kali toolset inside Docker;
# there is no prebuilt-binary variant for it.
ifeq ($(PLATFORM_VARIANT),allinone)
PLATFORM_PACKAGING := docker
endif
ifneq (,$(filter image-platform,$(MAKECMDGOALS)))
$(call require_one_of,PLATFORM_VARIANT,$(PLATFORM_VARIANT),slim allinone)
$(call require_one_of,PLATFORM_PACKAGING,$(PLATFORM_PACKAGING),native docker)
ifeq ($(PLATFORM_PACKAGING),native)
ifeq ($(PLATFORM_VARIANT),allinone)
$(error PLATFORM_PACKAGING=native supports only PLATFORM_VARIANT=slim; all-in-one must be built inside Docker)
endif
endif
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
ifeq ($(PLATFORM_PACKAGING),native)
ifeq ($(PLATFORM_VARIANT),slim)
PLATFORM_BUILD_TARGET := platform-slim-prebuilt
image-platform: build
endif
else
PLATFORM_BUILD_TARGET := platform-$(PLATFORM_VARIANT)
endif
PLATFORM_BUILD_TARGET ?= platform-$(PLATFORM_VARIANT)

image-platform:
	@echo "Building platform image $(PLATFORM_VARIANT): $(PLATFORM_IMAGE)-$(PLATFORM_VARIANT) (target=$(PLATFORM_BUILD_TARGET))"
	docker build -f Dockerfile --target $(PLATFORM_BUILD_TARGET) -t $(PLATFORM_IMAGE)-$(PLATFORM_VARIANT) $(DOCKER_BUILD_ARGS) --build-arg VERSION=$(VERSION) .

# Toolbox image: <toolbox-repo>:<version>-<profile>.
#
# The dev loop rebuilds this image far more often than its inputs change. The
# hash covers the recipe + helper sources + base digests
# (tools/toolbox/build-inputs.sh); when the local image already carries that
# hash, docker build is skipped entirely. Editing platform code (internal/,
# cmd/xalgorix) never invalidates the image, and editing a helper only re-runs
# the cheap final COPY layer because the Dockerfile keeps the profile installs
# below the gobuild outputs. TOOLBOX_FORCE=1 rebuilds regardless.
TOOLBOX_FORCE ?=
image-toolbox:
	@want=$$(tools/toolbox/build-inputs.sh "$(TOOLBOX_PROFILE)" "$(KALI_MIRROR)") || { echo "image-toolbox: cannot hash build inputs" >&2; exit 1; }; \
	have=$$(docker image inspect --format '{{ index .Config.Labels "xalgorix.build_inputs" }}' \
		"$(TOOLBOX_IMAGE)-$(TOOLBOX_PROFILE)" 2>/dev/null || true); \
	if [ "$(TOOLBOX_FORCE)" != "1" ] && [ -n "$$want" ] && [ -n "$$have" ] && [ "$$want" = "$$have" ]; then \
		echo "toolbox image $(TOOLBOX_IMAGE)-$(TOOLBOX_PROFILE) is current (inputs $$want); skipping build"; \
	else \
		echo "Building toolbox image $(TOOLBOX_PROFILE): $(TOOLBOX_IMAGE)-$(TOOLBOX_PROFILE)"; \
		docker build -f tools/toolbox/Dockerfile --target $(TOOLBOX_PROFILE) -t "$(TOOLBOX_IMAGE)-$(TOOLBOX_PROFILE)" \
			$(DOCKER_BUILD_ARGS) --label "xalgorix.build_inputs=$$want" .; \
	fi

# Host-side runtime service used by the recommended split deployment.
image-runnerd:
	@echo "Building runnerd image: $(RUNNERD_IMAGE)"
	docker build -f Dockerfile --target runnerd -t $(RUNNERD_IMAGE) $(DOCKER_BUILD_ARGS) --build-arg VERSION=$(VERSION) .

# ── Stack selection ────────────────────────────────────────────────────────
ifeq ($(STACK_MODE),native)
STACK_BUILD_DEPS := build
else ifeq ($(STACK_MODE),hybrid)
STACK_BUILD_DEPS := build image-toolbox
else
STACK_BUILD_DEPS := build image-platform image-toolbox image-runnerd
endif

stack-plan:
	@echo "STACK_MODE=$(STACK_MODE)"
	@echo "platform: $(if $(filter split,$(STACK_MODE)),slim platform image,host binary)"
	@echo "tools: $(if $(filter native,$(STACK_MODE)),native (no toolbox image),per-project toolbox containers)"
	@echo "build deps: $(STACK_BUILD_DEPS)"
	@echo "platform env: XALGORIX_RUNTIME_BACKEND=$(if $(filter native,$(STACK_MODE)),native,docker)"
	@echo "toolbox profile: $(if $(filter native,$(STACK_MODE)),-,$(TOOLBOX_PROFILE))"
	@echo "docker required: $(if $(filter native,$(STACK_MODE)),no,yes)"

stack-build: $(STACK_BUILD_DEPS)

ifeq ($(STACK_MODE),native)
stack-run: build
	XALGORIX_RUNTIME_BACKEND=native ./build/xalgorix --web
else ifeq ($(STACK_MODE),hybrid)
stack-run: build
	XALGORIX_RUNTIME_BACKEND=docker \
	XALGORIX_TOOLBOX_IMAGE=$(TOOLBOX_IMAGE)-$(TOOLBOX_PROFILE) \
	XALGORIX_TOOLBOX_PROFILE=$(TOOLBOX_PROFILE) \
	./build/xalgorix --web
else
stack-run:
	@echo "Starting split stack (platform container + runnerd + toolbox containers)..."
	docker compose -f docker-compose.yml --profile split up -d
endif

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
	go build -trimpath -o $(BUILD_DIR)/httpworker ./cmd/httpworker
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
