# https://gist.github.com/thomaspoignant/5b72d579bd5f311904d973652180c705

GOCMD=go
GOTEST=$(GOCMD) test
GOVET=$(GOCMD) vet
BINARY_NAME=modbusgateway
DEV_CERT_DIR=./app/certs
DEV_CERT_FILE=$(DEV_CERT_DIR)/dev_cert.pem
DEV_KEY_FILE=$(DEV_CERT_DIR)/dev_key.pem
VERSION?=0.0.0
SERVICE_PORT?=3000
DOCKER_REGISTRY?= #if set it should finished by /
EXPORT_RESULT?=false # for CI please set EXPORT_RESULT to true

# Raspberry Pi Login / IP
PI_USER := pv
PI_HOST := pi400ssd
PI_PATH := /home/pv/

GREEN  := $(shell tput -Txterm setaf 2)
YELLOW := $(shell tput -Txterm setaf 3)
WHITE  := $(shell tput -Txterm setaf 7)
CYAN   := $(shell tput -Txterm setaf 6)
RESET  := $(shell tput -Txterm sgr0)


BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
BUILD_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

LDFLAGS := -X 'main.buildDate=$(BUILD_DATE)' \
           -X 'main.buildCommit=$(BUILD_COMMIT)'

.PHONY: all test build vendor copy build_dev build_arm6 build_arm7 build_arm64 build_windows386 build_windows64 build_linux386 build_linux64 build_mac_arm64 deploy clean help ensure_dev_certs

all: help

clean: ## Remove build related file
	rm -fr ./bin/arm6
	rm -fr ./bin/arm7
	rm -fr ./bin/arm8
	rm -fr ./bin/arm64
	rm -fr ./bin/amd64
	rm -fr ./bin/darwin
	rm -fr ./bin/386

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
# Raspberry Pi Kompatibilitätstabelle für GOARCH und GOARM
# ==================================================================================================================
# Modell                    CPU    GOARCH=arm GOARM=6   GOARCH=arm GOARM=7   GOARCH=arm GOARM=8   GOARCH=arm64
#
# Raspberry Pi 1 (A/B/+)    ARMv6  Läuft gut           Nicht kompatibel     Nicht kompatibel     Nicht kompatibel
# Raspberry Pi Zero (1.Gen) ARMv6  Läuft gut           Nicht kompatibel     Nicht kompatibel     Nicht kompatibel
# Raspberry Pi 2            ARMv7  Langsam             Läuft gut            Nicht kompatibel     Nicht kompatibel
# Raspberry Pi 3            ARMv8  Langsam             Läuft gut            Läuft gut            Läuft mit 64-Bit OS
# Raspberry Pi 4            ARMv8  Langsam             Läuft gut            Läuft gut            Läuft mit 64-Bit OS
# Raspberry Pi 5            ARMv8  Nicht kompatibel    Langsam              Läuft gut            Läuft mit 64-Bit OS
# Raspberry Pi Zero 2 W     ARMv8  Langsam             Läuft gut            Läuft gut            Läuft mit 64-Bit OS
# ==================================================================================================================


build_arm64_dev: ensure_dev_certs ## build binary for raspberry models 3/4/5/Zero2 64bit with Swagger UI
	GOOS=linux GOARCH=arm64 \
	go build -tags swagger -ldflags "$(LDFLAGS)" -o ./bin/arm64/${BINARY_NAME} ./cmd/main.go

build_arm6: ensure_dev_certs ## build binary for all raspberry models 32bit except Pi5
	GOOS=linux GOARCH=arm GOARM=6 \
	go build -ldflags "$(LDFLAGS)" -o ./bin/arm6/${BINARY_NAME} ./cmd/main.go

build_arm7: ensure_dev_certs ## build binary for raspberry models 2/3/4/5/Zero2 32bit
	GOOS=linux GOARCH=arm GOARM=7 \
	go build -ldflags "$(LDFLAGS)" -o ./bin/arm7/${BINARY_NAME} ./cmd/main.go

build_arm8: ensure_dev_certs ## build binary for raspberry models 3/4/5/Zero2 32bit
	GOOS=linux GOARCH=arm GOARM=7 \
	go build -ldflags "$(LDFLAGS)" -o ./bin/arm8/${BINARY_NAME} ./cmd/main.go

build_arm64: ensure_dev_certs ## build binary for raspberry models 3/4/5/Zero2 64bit
	GOOS=linux GOARCH=arm64 \
	go build -ldflags "$(LDFLAGS)" -o ./bin/arm64/${BINARY_NAME} ./cmd/main.go

build_windows386: ensure_dev_certs ## build binary for windows
	GOOS=windows GOARCH=386 \
	go build -ldflags "$(LDFLAGS)" -o ./bin/386/${BINARY_NAME}.exe ./cmd/main.go

build_windows64: ensure_dev_certs ## build binary for windows 64bit
	GOOS=windows GOARCH=amd64 \
	go build -ldflags "$(LDFLAGS)" -o ./bin/amd64/${BINARY_NAME}.exe ./cmd/main.go

build_linux386: ensure_dev_certs ## build binary for linux
	GOOS=linux GOARCH=386 \
	go build -ldflags "$(LDFLAGS)" -o ./bin/386/${BINARY_NAME} ./cmd/main.go

build_linux64: ensure_dev_certs ## build binary for linux 64bit
	GOOS=linux GOARCH=amd64 \
	go build -ldflags "$(LDFLAGS)" -o ./bin/amd64/${BINARY_NAME} ./cmd/main.go

build_mac_arm64: ensure_dev_certs ## build binary mac M1
	GOOS=darwin GOARCH=arm64 \
	go build -ldflags "$(LDFLAGS)" -o ./bin/darwin/${BINARY_NAME} ./cmd/main.go


deploy: build_arm64 ## build binary and copy binary to ${TARGET_NODE}:/tmp
	@echo "Copying binary to  $(PI_USER)@$(PI_HOST):$(PI_PATH)"
	scp ./bin/arm64/${BINARY_NAME} $(PI_USER)@$(PI_HOST):$(PI_PATH)

deploy_dev: build_arm64_dev ## build binary and copy binary to ${TARGET_NODE}:/tmp
	@echo "Copying binary to  $(PI_USER)@$(PI_HOST):$(PI_PATH)"
	scp ./bin/arm64/${BINARY_NAME} $(PI_USER)@$(PI_HOST):$(PI_PATH)


## Help:
help: ## Show this help.
	@echo ''
	@echo 'Usage:'
	@echo '  ${YELLOW}make${RESET} ${GREEN}<target>${RESET}'
	@echo ''
	@echo 'Targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[0-9a-zA-Z_-]+:.*?## / {printf "${YELLOW}%-16s${GREEN}%s${RESET}\n", $$1, $$2}' $(MAKEFILE_LIST)