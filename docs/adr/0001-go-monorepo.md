# ADR 0001: Go, single module, several binaries

Status: accepted

## Decision

One Go module (`go.mod` at the root) that builds several binaries from `cmd/`.

## Why

- The sing-box ecosystem and uTLS are Go libraries; using Go avoids bridging.
- Go produces static, cross-compiled binaries for Windows, Linux and OpenWrt targets (amd64, arm64, mipsle).
- Router RAM is small; one static binary with no runtime is the cheapest to ship.
- One module keeps shared code (`internal/lib`) in one place with one version.
