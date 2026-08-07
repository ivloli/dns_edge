.PHONY: all build test tidy clean install uninstall start stop restart status \
	prepare-release build-release release-package release-checksum \
	run-local stop-local restart-local deploy-local status-local

APP_NAME := dns-edge
BIN_DIR := bin
BIN_PATH := $(BIN_DIR)/$(APP_NAME)
TARGET_OS ?= linux
TARGET_ARCH ?= amd64
RELEASE_TAG ?= v$(shell grep -oP 'Version\s*=\s*"\K[0-9.]+' internal/version/version.go)
RELEASE_DIR := release
RELEASE_INFIX ?=
RELEASE_NAME := $(APP_NAME)$(if $(RELEASE_INFIX),-$(RELEASE_INFIX),)-$(TARGET_OS)-$(TARGET_ARCH)-$(RELEASE_TAG)
RELEASE_PATH := $(RELEASE_DIR)/$(RELEASE_NAME)
RELEASE_BIN := $(RELEASE_PATH)/$(APP_NAME)
RELEASE_TAR := $(RELEASE_NAME).tar.gz
# Corefile is intentionally gitignored (it holds real connection secrets on
# machines that have one) — a fresh clone will never have it. Fall back to
# the tracked, placeholder-only Corefile.example so packaging still works
# out of the box; machines with a real local Corefile keep using it.
CONFIG_SRC ?= $(if $(wildcard Corefile),Corefile,Corefile.example)

RUN_DIR := .run
LOCAL_CONFIG ?= Corefile.local
LOCAL_PID := $(RUN_DIR)/dns-edge.pid
LOCAL_LOG := $(RUN_DIR)/dns-edge.log
LOCAL_HEALTHZ ?= http://127.0.0.1:8080/healthz

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

# ── local (no-sudo, no-systemd) dev/test-env targets ────────────────────────
# For boxes without passwordless sudo, where the systemd-based install/start/
# stop/restart targets below can't run. Tracks the process via a PID file
# under $(RUN_DIR) instead of a system service.

run-local: build
	@install -d -m 755 $(RUN_DIR)
	@if [ -f $(LOCAL_PID) ] && kill -0 "$$(cat $(LOCAL_PID))" 2>/dev/null; then \
		echo "$(APP_NAME) already running (pid $$(cat $(LOCAL_PID)))"; \
	else \
		nohup $(BIN_PATH) -config $(LOCAL_CONFIG) > $(LOCAL_LOG) 2>&1 & \
		echo $$! > $(LOCAL_PID); \
		sleep 1; \
		echo "Started $(APP_NAME) (pid $$(cat $(LOCAL_PID))), log: $(LOCAL_LOG)"; \
	fi

stop-local:
	@if [ -f $(LOCAL_PID) ]; then \
		pid="$$(cat $(LOCAL_PID))"; \
		if kill -0 "$$pid" 2>/dev/null; then \
			kill "$$pid" && echo "Stopped $(APP_NAME) (pid $$pid)"; \
		else \
			echo "$(APP_NAME) not running (stale pid $$pid)"; \
		fi; \
		rm -f $(LOCAL_PID); \
	else \
		echo "$(APP_NAME) not running (no pid file)"; \
	fi

restart-local: stop-local run-local

deploy-local: restart-local
	@sleep 1
	@curl -sf $(LOCAL_HEALTHZ) >/dev/null \
		&& echo "$(APP_NAME) healthz OK ($(LOCAL_HEALTHZ))" \
		|| (echo "$(APP_NAME) healthz FAILED ($(LOCAL_HEALTHZ))"; exit 1)

status-local:
	@if [ -f $(LOCAL_PID) ] && kill -0 "$$(cat $(LOCAL_PID))" 2>/dev/null; then \
		echo "$(APP_NAME) running (pid $$(cat $(LOCAL_PID)))"; \
	else \
		echo "$(APP_NAME) not running"; \
	fi

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
