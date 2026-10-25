MODULE  := $(shell go list -m)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS := -X $(MODULE)/internal/lib/version.Version=$(VERSION) -X $(MODULE)/internal/lib/version.Commit=$(COMMIT)
CMDS    := $(notdir $(wildcard cmd/*))

.PHONY: build test test-lab arch vet lint cross clean fetch fetch-check e2e lab-clean

build:
	@mkdir -p bin
	@for c in $(CMDS); do go build -ldflags "$(LDFLAGS)" -o bin/$$c ./cmd/$$c || exit 1; done

test:
	go test ./...

test-lab:
	go test -tags lab ./...

arch:
	go test -run TestArchitecture .

vet:
	go vet ./...

lint:
	golangci-lint run

cross:
	GOOS=windows GOARCH=amd64 go build ./...
	GOOS=linux GOARCH=amd64 go build ./...
	GOOS=linux GOARCH=arm64 go build ./...
	GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build ./...

# Download and verify the pinned third-party binaries (third_party/manifest.yaml).
fetch:
	go run ./scripts/fetch

# Verify installed third-party binaries without network access.
fetch-check:
	go run ./scripts/fetch -check

clean:
	rm -rf bin dist

# End-to-end lab in Linux network namespaces (WSL2 or Linux, root). See docs/RELEASE.md.
# The human runs: for m in nfnetlink_queue nft_queue tun; do sudo modprobe $m; done   (one module per call; not persistent in WSL2), then this target.
e2e:
	@if [ "$$(uname -s)" != "Linux" ]; then echo "make e2e: needs Linux (WSL2 on Windows); see docs/RELEASE.md" >&2; exit 1; fi
	@if [ "$$(id -u)" != "0" ]; then echo 'make e2e: needs root. Run: sudo -E env "PATH=$$PATH" make e2e' >&2; exit 1; fi
	@$(MAKE) build
	LAB_BIN_DIR=$(CURDIR)/bin go test -tags lab -v -count=1 -timeout 60m ./lab/...
	@echo "report: $(CURDIR)/$$(ls -dt dist/e2e/*/ | head -1)report.md"

# Remove namespaces (obl????-*) and /etc/netns/obl????-* left by a crashed lab run.
lab-clean:
	@if [ "$$(uname -s)" != "Linux" ]; then echo "make lab-clean: needs Linux (WSL2)" >&2; exit 1; fi
	@if [ "$$(id -u)" != "0" ]; then echo 'make lab-clean: needs root. Run: sudo -E env "PATH=$$PATH" make lab-clean' >&2; exit 1; fi
	LAB_CLEAN=1 go test -tags lab -count=1 -run TestCleanLeftovers ./lab/
