BINARY ?= speedmux
BUILD_DIR ?= build
LOCAL_BIN ?= $(HOME)/.local/bin
GO ?= go

.PHONY: help build deploy install uninstall clean

help:
	@echo "Targets:"
	@echo "  make deploy    Build and install $(BINARY) to $(LOCAL_BIN)"
	@echo "  make build     Build binary into $(BUILD_DIR)/$(BINARY)"
	@echo "  make uninstall Remove $(LOCAL_BIN)/$(BINARY)"
	@echo "  make clean     Remove build artifacts"

build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -o $(BUILD_DIR)/$(BINARY) .

deploy: install

install: build
	@mkdir -p $(LOCAL_BIN)
	@install -m 0755 $(BUILD_DIR)/$(BINARY) $(LOCAL_BIN)/$(BINARY)
	@echo "Installed $(BINARY) to $(LOCAL_BIN)/$(BINARY)"
	@if echo ":$$PATH:" | grep -q ":$(LOCAL_BIN):"; then \
		echo "$(LOCAL_BIN) is already in PATH"; \
	else \
		echo "$(LOCAL_BIN) is not in PATH. Add this to your shell config:"; \
		echo "  export PATH=\"$(LOCAL_BIN):\$$PATH\""; \
	fi

uninstall:
	@rm -f $(LOCAL_BIN)/$(BINARY)
	@echo "Removed $(LOCAL_BIN)/$(BINARY)"

clean:
	@rm -rf $(BUILD_DIR)
