.PHONY: all build test tidy clean install uninstall start stop restart status \
	prepare-release build-release release-package release-checksum

APP_NAME := dns-edge
BIN_DIR := bin
BIN_PATH := $(BIN_DIR)/$(APP_NAME)
TARGET_OS ?= linux
TARGET_ARCH ?= amd64
RELEASE_TAG ?= $(shell git describe --always --dirty --tags 2>/dev/null || date +%Y%m%d%H%M%S)
RELEASE_DIR := release
RELEASE_INFIX ?=
RELEASE_NAME := $(APP_NAME)$(if $(RELEASE_INFIX),-$(RELEASE_INFIX),)-$(TARGET_OS)-$(TARGET_ARCH)-$(RELEASE_TAG)
RELEASE_PATH := $(RELEASE_DIR)/$(RELEASE_NAME)
RELEASE_BIN := $(RELEASE_PATH)/$(APP_NAME)
RELEASE_TAR := $(RELEASE_NAME).tar.gz
CONFIG_SRC ?= Corefile

PREFIX ?= /opt/dns-edge
INSTALL_BIN := $(PREFIX)/bin/$(APP_NAME)
ETC_DIR ?= $(PREFIX)/etc
DATA_DIR ?= $(PREFIX)/data
SERVICE_DIR ?= /etc/systemd/system

all: build

build:
	install -d -m 755 $(BIN_DIR)
	GOWORK=off go build -o $(BIN_PATH) ./cmd/dns-edge
	@echo "Built $(BIN_PATH)"

test:
	GOWORK=off go test -race -count=1 ./...

tidy:
	GOWORK=off go mod tidy

clean:
	rm -rf $(BIN_DIR) $(RELEASE_DIR)

install:
	@echo "Installing $(APP_NAME)..."
	@set -e; \
	src_bin=""; \
	if [ -f "$(BIN_PATH)" ]; then \
		src_bin="$(BIN_PATH)"; \
	elif [ -f "$(APP_NAME)" ]; then \
		src_bin="$(APP_NAME)"; \
	else \
		$(MAKE) build; \
		src_bin="$(BIN_PATH)"; \
	fi; \
	systemctl stop dns-edge 2>/dev/null || true; \
	install -d -m 755 $(PREFIX)/bin; \
	install -m 755 "$$src_bin" $(INSTALL_BIN)
	install -d -m 755 $(ETC_DIR)
	install -d -m 755 $(DATA_DIR)
	install -m 644 "$(CONFIG_SRC)" $(ETC_DIR)/Corefile
	[ -f $(ETC_DIR)/env ] || install -m 600 /dev/null $(ETC_DIR)/env
	@sed -e 's|/opt/dns-edge/bin|$(PREFIX)/bin|g' \
	     -e 's|/opt/dns-edge/etc|$(ETC_DIR)|g' \
	     -e 's|/opt/dns-edge/data|$(DATA_DIR)|g' \
	     dns-edge.service > $(SERVICE_DIR)/dns-edge.service
	@chmod 644 $(SERVICE_DIR)/dns-edge.service
	systemctl daemon-reload
	systemctl enable dns-edge
	systemctl start dns-edge
	@echo "Installed $(APP_NAME). Service started."
	@echo "Binary:  $(INSTALL_BIN)"
	@echo "Config:  $(ETC_DIR)/Corefile"
	@echo "WorkDir: $(DATA_DIR)"

uninstall:
	systemctl stop dns-edge 2>/dev/null || true
	systemctl disable dns-edge 2>/dev/null || true
	rm -f $(SERVICE_DIR)/dns-edge.service $(INSTALL_BIN)
	systemctl daemon-reload
	@echo "Uninstalled $(APP_NAME). Config files in $(ETC_DIR) are preserved."

start:
	systemctl start dns-edge

stop:
	systemctl stop dns-edge

restart:
	systemctl restart dns-edge

status:
	systemctl status dns-edge

prepare-release:
	install -d -m 755 $(RELEASE_PATH)

build-release: prepare-release
	@echo "Building release for $(TARGET_OS)/$(TARGET_ARCH)..."
	GOWORK=off CGO_ENABLED=0 GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH) go build -trimpath -ldflags="-s -w" -o $(RELEASE_BIN) ./cmd/dns-edge
	install -m 644 "$(CONFIG_SRC)" $(RELEASE_PATH)/Corefile
	@if [ -f "Corefile.example" ]; then install -m 644 Corefile.example $(RELEASE_PATH)/Corefile.example; fi
	install -m 644 dns-edge.service $(RELEASE_PATH)/dns-edge.service
	install -m 644 Makefile $(RELEASE_PATH)/Makefile
	install -m 644 README.md $(RELEASE_PATH)/README.md
	@echo "Prepared release directory: $(RELEASE_PATH)"

release-package: build-release
	@echo "Creating release tarball..."
	COPYFILE_DISABLE=1 COPY_EXTENDED_ATTRIBUTES_DISABLE=1 tar --no-xattrs -czf $(RELEASE_TAR) -C $(RELEASE_DIR) $(RELEASE_NAME)
	@echo "Created $(RELEASE_TAR)"

release-checksum: release-package
	shasum -a 256 $(RELEASE_TAR)
	@echo "Release package ready: $(RELEASE_TAR)"
