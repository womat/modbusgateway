BINARY_NAME=modbusgateway
DEV_CERT_DIR=./app/certs
DEV_CERT_FILE=$(DEV_CERT_DIR)/dev_cert.pem
DEV_KEY_FILE=$(DEV_CERT_DIR)/dev_key.pem

# Branch policy: there is one branch, RELEASE_BRANCH; work is committed to it and
# releases are tags on it. The release target only tags a clean RELEASE_BRANCH that
# matches origin, because a tag on a commit that never reached it produces
# artifacts that no longer match what the repository publishes.
RELEASE_BRANCH ?= main

# Raspberry Pi Login / IP. These are placeholders - the real host name stays out
# of this public repository. Set your device as environment variables instead,
# so plain `make deploy` works without repeating the address (fish: set -Ux,
# bash/zsh: export in the shell profile):
#
#   PI_USER=myuser
#   PI_HOST=mypi
#
# A project-specific device can go into Makefile.local (gitignored, included
# below) with the same names; command-line values override both.
#
# PI_PATH defaults to the login directory, which is correct for any user name;
# override it only to land the binary somewhere else.
PI_USER ?= pi
PI_HOST ?= raspberrypi
PI_PATH ?= .

# Architecture of the target device, used by every deploy_* target.
# arm6 runs on every Pi in 32-bit mode, including a Pi Zero (1st gen), which is
# ARMv6 and 32-bit only - an arm64 binary does not start on it. Override for a
# 64-bit build:
#   make deploy PI_ARCH=arm64 PI_HOST=my-pi
# See the compatibility table below for which value a model needs.
PI_ARCH ?= arm6

# Local, untracked overrides for the settings above. Missing file is fine.
-include Makefile.local

# Guard the value: PI_REL_ARCH below maps anything unrecognised to arm64, so a
# typo would otherwise silently produce or download the wrong architecture.
ifeq ($(filter $(PI_ARCH),arm6 arm7 arm64),)
$(error PI_ARCH must be one of: arm6 arm7 arm64 (got '$(PI_ARCH)'))
endif

# Release archives are named after the Go arch, not the make target.
PI_REL_ARCH := $(if $(filter arm6,$(PI_ARCH)),armv6,$(if $(filter arm7,$(PI_ARCH)),armv7,arm64))

GREEN  := $(shell tput -Txterm setaf 2)
YELLOW := $(shell tput -Txterm setaf 3)
WHITE  := $(shell tput -Txterm setaf 7)
CYAN   := $(shell tput -Txterm setaf 6)
RESET  := $(shell tput -Txterm sgr0)


BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
BUILD_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# The Git tag is the single source of truth for the version. A build on a tag
# yields a plain semver string (1.7.0), any other working copy a descriptive
# fallback (1.7.0-5-g0c13781-dirty).
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
VERSION := $(if $(VERSION),$(VERSION),dev)

LDFLAGS := -X 'main.buildDate=$(BUILD_DATE)' \
           -X 'main.buildCommit=$(BUILD_COMMIT)' \
           -X 'github.com/womat/modbusgateway/app.VERSION=$(VERSION)'

.PHONY: all test lint release deploy_release deploy deploy_dev clean help ensure_dev_certs

all: help

clean: ## Remove build related file
	rm -fr ./bin/release
	rm -fr ./bin/arm6
	rm -fr ./bin/arm7
	rm -fr ./bin/arm64
	rm -fr ./dist

# Pure Go: the tests run on any platform. The RTU listener test needs socat and
# Linux; it is skipped elsewhere.
test: ensure_dev_certs ## run all tests with the race detector
	go test -race ./...

# The analysers are pinned like in the workflows (dependabot does not see them) and
# installed into ./bin/tools for this machine, then run against the deployment
# target: GOOS=linux brings in the Linux-only RTU listener test, and `go run` with
# a target GOOS/GOARCH would build a tool binary this machine cannot execute.
# Both build variants are linted, with and without the Swagger UI.
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION   := v1.8.0
TOOLS_BIN             := $(CURDIR)/bin/tools

lint: ensure_dev_certs ## gofmt, go vet, golangci-lint and govulncheck, for the deployment target ($(PI_ARCH))
	@test -z "$$(gofmt -l ./app ./cmd ./pkg)" || { echo "not gofmt'ed:"; gofmt -l ./app ./cmd ./pkg; exit 1; }
	GOBIN=$(TOOLS_BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	GOBIN=$(TOOLS_BIN) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GOENV_$(PI_ARCH)) go vet ./...
	$(GOENV_$(PI_ARCH)) go vet -tags swagger ./...
	$(GOENV_$(PI_ARCH)) $(TOOLS_BIN)/golangci-lint run ./...
	$(GOENV_$(PI_ARCH)) $(TOOLS_BIN)/golangci-lint run --build-tags swagger ./...
	$(GOENV_$(PI_ARCH)) $(TOOLS_BIN)/govulncheck ./...

ensure_dev_certs:
	@mkdir -p $(DEV_CERT_DIR)
	@if [ ! -f "$(DEV_CERT_FILE)" ] || [ ! -f "$(DEV_KEY_FILE)" ]; then \
		echo "Generating development TLS certificate in $(DEV_CERT_DIR)"; \
		openssl req -x509 -nodes -newkey rsa:2048 \
			-keyout "$(DEV_KEY_FILE)" \
			-out "$(DEV_CERT_FILE)" \
			-days 365 \
			-subj "/C=AT/ST=Vienna/L=Vienna/O=modbusgateway/OU=Development/CN=localhost"; \
	fi


# ==================================================================================================================
# Raspberry Pi -> Go build target
# Sources: https://go.dev/wiki/GoArm and `go help environment` (GOARM, GOARM64).
# ==================================================================================================================
# GOARM applies to GOARCH=arm only and accepts 5, 6 or 7 - there is no GOARM=8.
# 64-bit ARM is GOARCH=arm64, where GOARM is ignored and GOARM64 (default v8.0)
# applies instead. When cross-compiling, GOARM defaults to 7, so a GOARCH=arm
# target that omits it silently builds for ARMv7. Floating point follows the
# value: ,hardfloat is the default for 6 and 7, ,softfloat the default for 5.
#
# Model                     CPU core       ISA         32-bit OS   64-bit OS
#
# Raspberry Pi 1 (A/B/+)    ARM1176JZF-S   ARMv6       GOARM=6     -
# Raspberry Pi Zero / W     ARM1176JZF-S   ARMv6       GOARM=6     -
# Raspberry Pi 2            Cortex-A7      ARMv7       GOARM=7     -
# Raspberry Pi 3            Cortex-A53     ARMv8-A     GOARM=7     GOARCH=arm64
# Raspberry Pi 4            Cortex-A72     ARMv8-A     GOARM=7     GOARCH=arm64
# Raspberry Pi 5            Cortex-A76     ARMv8.2-A   GOARM=7     GOARCH=arm64
# Raspberry Pi Zero 2 W     Cortex-A53     ARMv8-A     GOARM=7     GOARCH=arm64
#
# These are the minimum values a model needs. ARM is backward compatible, so a
# GOARM=6 binary also runs on ARMv7/ARMv8 in 32-bit mode; the reverse never
# works. arm6 therefore covers every supported board, hence PI_ARCH=arm6.
# ==================================================================================================================


# Go environment per target, see the table above. build_<arch> and build_<arch>_dev
# (with Swagger UI) are generated from it by the static pattern rules below.
GOENV_arm6  := GOOS=linux GOARCH=arm GOARM=6
GOENV_arm7  := GOOS=linux GOARCH=arm GOARM=7
GOENV_arm64 := GOOS=linux GOARCH=arm64

BUILD_ARCHS       := arm6 arm7 arm64
BUILD_TARGETS     := $(addprefix build_,$(BUILD_ARCHS))
BUILD_DEV_TARGETS := $(addsuffix _dev,$(BUILD_TARGETS))

# Declared here, not in the .PHONY list at the top: prerequisites of a rule are expanded
# when read, and these variables are only defined at this point.
.PHONY: $(BUILD_TARGETS) $(BUILD_DEV_TARGETS)

# Help texts for the generated targets; `make help` reads these comment lines too.
## build_arm6:      ## ARMv6, runs on every Pi in 32-bit mode; required for Pi 1 / Zero
## build_arm7:      ## ARMv7, 32-bit Pi 2 and newer
## build_arm64:     ## ARMv8, 64-bit OS on Pi 3/4/5/Zero 2 W
## build_<arch>_dev: ## the same with Swagger UI (dev only), e.g. build_arm6_dev

# Static pattern rules (explicit target lists) instead of plain build_% rules: they work
# with .PHONY and resolve build_arm6_dev unambiguously, also in GNU Make 3.81 on macOS.
$(BUILD_TARGETS): build_%: ensure_dev_certs
	$(GOENV_$*) go build -ldflags "$(LDFLAGS)" -o ./bin/$*/${BINARY_NAME} ./cmd/main.go

$(BUILD_DEV_TARGETS): build_%_dev: ensure_dev_certs
	$(GOENV_$*) go build -tags swagger -ldflags "$(LDFLAGS)" -o ./bin/$*/${BINARY_NAME} ./cmd/main.go


deploy: build_$(PI_ARCH) ## build for $(PI_ARCH) and copy to $(PI_USER)@$(PI_HOST) (override: make deploy PI_ARCH=arm64 PI_HOST=my-pi)
	@echo "Copying $(PI_ARCH) binary to  $(PI_USER)@$(PI_HOST):$(PI_PATH)"
	scp ./bin/$(PI_ARCH)/${BINARY_NAME} $(PI_USER)@$(PI_HOST):$(PI_PATH)

deploy_dev: build_$(PI_ARCH)_dev ## build for $(PI_ARCH) with Swagger UI and copy to $(PI_USER)@$(PI_HOST)
	@echo "Copying $(PI_ARCH) binary (Swagger UI) to  $(PI_USER)@$(PI_HOST):$(PI_PATH)"
	scp ./bin/$(PI_ARCH)/${BINARY_NAME} $(PI_USER)@$(PI_HOST):$(PI_PATH)

# Checksum verification differs between GNU coreutils and the BSD/perl shasum on macOS.
SHA256SUM := $(shell command -v sha256sum 2>/dev/null || echo "shasum -a 256")

deploy_release: ## download a published $(PI_REL_ARCH) release, verify it and copy it to the Pi (make deploy_release TAG=v1.7.0)
	@test -n "$(TAG)" || { echo "usage: make deploy_release TAG=v1.7.0"; exit 1; }
	@command -v gh >/dev/null || { echo "gh is required, see https://cli.github.com"; exit 1; }
	rm -fr ./bin/release
	mkdir -p ./bin/release
	gh release download $(TAG) --dir ./bin/release \
		--pattern '*_linux_$(PI_REL_ARCH).tar.gz' --pattern 'checksums.txt'
	cd ./bin/release && $(SHA256SUM) -c checksums.txt --ignore-missing
	tar xzf ./bin/release/*_linux_$(PI_REL_ARCH).tar.gz -C ./bin/release ${BINARY_NAME}
	@echo "Copying $(TAG) binary to  $(PI_USER)@$(PI_HOST):$(PI_PATH)"
	scp ./bin/release/${BINARY_NAME} $(PI_USER)@$(PI_HOST):$(PI_PATH)


release: ## tag $(RELEASE_BRANCH) and push it, triggering the GitHub release workflow (make release TAG=v1.7.0)
	@test -n "$(TAG)" || { echo "usage: make release TAG=v1.7.0"; exit 1; }
	@echo "$(TAG)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "TAG must be semver with a v prefix, e.g. v1.7.0"; exit 1; }
	@git diff --quiet HEAD || { echo "working tree is dirty, commit first"; exit 1; }
	@test "$$(git rev-parse --abbrev-ref HEAD)" = "$(RELEASE_BRANCH)" || \
		{ echo "releases are cut from $(RELEASE_BRANCH), but you are on $$(git rev-parse --abbrev-ref HEAD) - run: git checkout $(RELEASE_BRANCH)"; exit 1; }
	@git fetch origin --quiet
	@test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/$(RELEASE_BRANCH))" || \
		{ echo "$(RELEASE_BRANCH) and origin/$(RELEASE_BRANCH) differ - pull or push first"; exit 1; }
	git tag -a $(TAG) -m "release $(TAG)"
	git push origin $(TAG)
	@echo "Tag pushed. Watch the release build with: gh run watch"


## Help:

# awk reads the help comments straight from the file, so make never expands the
# variables in them. Substitute the ones used in help text afterwards, otherwise
# the listing shows a literal $(PI_ARCH) instead of its current value.
HELP_VARS := PI_ARCH PI_REL_ARCH PI_USER PI_HOST PI_PATH BINARY_NAME
HELP_SED := $(foreach v,$(HELP_VARS),-e 's|[$$][({]$(v)[)}]|$($(v))|g')

help: ## Show this help.
	@echo ''
	@echo 'Usage:'
	@echo '  ${YELLOW}make${RESET} ${GREEN}<target>${RESET}'
	@echo ''
	@echo 'Targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^(## )?[0-9a-zA-Z_<>-]+:.*?## / {sub(/^## /, "", $$1); printf "${YELLOW}%-17s${GREEN}%s${RESET}\n", $$1, $$2}' $(MAKEFILE_LIST) \
		| sed $(HELP_SED)