MODULE  := $(shell go list -m)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS := -X $(MODULE)/internal/lib/version.Version=$(VERSION) -X $(MODULE)/internal/lib/version.Commit=$(COMMIT)
CMDS    := $(notdir $(wildcard cmd/*))

.PHONY: build test test-lab arch vet lint cross clean fetch fetch-check

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
