# obhod (traffic-splitter)

Traffic splitter: it probes the local network to find which domains are blocked and how, decides per domain whether to go direct, use a desync engine, a tunnel, a proxy or block, and applies that on a desktop (sing-box plus a desync engine) or on an OpenWrt router (podkop domain lists). All logs, decisions and reports stay local.

**Early development — skeleton only.**

## Binaries

| Binary | App module |
|---|---|
| `blockanalyzer` | `internal/blockanalyzer` |
| `splitter` | `internal/pc` |
| `splitter-router` | `internal/router` |

## Layout

The code is a tree of modules; each folder has an `API.md` contract, and a module may import only its direct children and the `lib/` folders of its ancestors. This is enforced by `arch_test.go`; see [docs/architecture.md](docs/architecture.md).

## Commands

| make | plain go |
|---|---|
| `make build` | `go build ./cmd/...` |
| `make test` | `go test ./...` |
| `make test-lab` | `go test -tags lab ./...` |
| `make arch` | `go test -run TestArchitecture .` |
| `make vet` | `go vet ./...` |
| `make lint` | `golangci-lint run` |
| `make cross` | `GOOS=linux GOARCH=arm64 go build ./...` (and windows/amd64, linux/amd64, linux/mipsle) |
| `make clean` | remove `bin/` and `dist/` |
