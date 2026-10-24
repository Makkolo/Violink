# Violink build
#
#   make            build for Linux and Windows into build/
#   make linux      only Linux  -> build/linux-amd64/violink
#   make windows    only Windows -> build/windows-amd64/violink.exe
#   make run        build for Linux and run it in the console
#   make release    zip/tar.gz packages in release/ (what GitHub Actions does for a tag)
#   make deps       download dependencies and update go.sum (run once after cloning)
#   make winres     regenerate the Windows icon/version resource
#   make clean      remove build/ and release/
#
# The build folders get a copy of runtime/ (config, scripts, simulations)
# next to the executable. Files already there are NOT overwritten, so the
# config and scripts you edit while testing survive a rebuild.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
WINVER  := $(patsubst v%,%,$(VERSION))
GO      ?= go
PKG     := ./cmd/violink

export CGO_ENABLED = 0

.PHONY: all linux windows run release deps winres clean clean-release

all: linux windows

linux:
	GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o build/linux-amd64/violink $(PKG)
	tools/copy-runtime.sh build/linux-amd64 linux
	@echo "built build/linux-amd64/violink ($(VERSION))"

# -H windowsgui: no console window, Violink only lives in the system tray.
windows:
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS) -H windowsgui" -o build/windows-amd64/violink.exe $(PKG)
	tools/copy-runtime.sh build/windows-amd64 windows
	@echo "built build/windows-amd64/violink.exe ($(VERSION))"

run: linux
	cd build/linux-amd64 && ./violink -console

# Fresh packages: new copies of the example files, nothing from build/.
release: clean-release
	GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o release/violink-$(VERSION)-linux-amd64/violink $(PKG)
	tools/copy-runtime.sh release/violink-$(VERSION)-linux-amd64 linux
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS) -H windowsgui" -o release/violink-$(VERSION)-windows-amd64/violink.exe $(PKG)
	tools/copy-runtime.sh release/violink-$(VERSION)-windows-amd64 windows
	cd release && tar -czf violink-$(VERSION)-linux-amd64.tar.gz violink-$(VERSION)-linux-amd64
	cd release && zip -qr violink-$(VERSION)-windows-amd64.zip violink-$(VERSION)-windows-amd64
	cd release && sha256sum *.tar.gz *.zip > SHA256SUMS.txt
	@ls -la release

deps:
	$(GO) mod tidy

# Needs network access. The committed rsrc_windows_amd64.syso works without this.
winres:
	$(GO) run github.com/tc-hib/go-winres@latest simply --arch amd64 \
		--icon assets/windows/violink.ico --manifest gui \
		--product-name Violink --file-description "Violink - scriptable I/O bridge" \
		--original-filename violink.exe --copyright "MIT License" \
		--product-version "$(WINVER)" --file-version "$(WINVER)" \
		--out cmd/violink/rsrc

clean-release:
	rm -rf release

clean: clean-release
	rm -rf build
