# Human plan — third-party binaries (sing-box, zapret2)

This is a checklist for **the human**. Agents must not download these binaries, run them with admin rights, or change system settings (root `AGENTS.md`, Hard rules). An agent can write code around them once the steps below are done.

## 1. What is needed and why

| Binary | Used by | Platforms | Comes from | Notes |
|---|---|---|---|---|
| `sing-box` | `internal/pc/singbox`, `procman` | windows-amd64, linux-amd64, linux-arm64 | official GitHub releases of SagerNet/sing-box (`sing-box-<ver>-<os>-<arch>.zip` / `.tar.gz`) | Routing layer: TUN, DNS, outbounds. One file. The Windows TUN needs admin. |
| `winws2.exe` + `WinDivert.dll` + `WinDivert64.sys` + `cygwin1.dll` | `internal/pc/desync/winws` | windows-amd64 | official GitHub releases of bol-van/**zapret2** (`zapret2-v<ver>.zip`, folder `binaries/windows-x86_64`) | These four files must sit **in the same folder**. WinDivert loads a signed kernel driver on first use, so it needs admin. |
| `nfqws2` | `internal/pc/desync/nfqws` | linux-amd64, linux-arm64 | the same zapret2 zip (`binaries/linux-x86_64`, `linux-arm64`) | Static binary. Needs root (or CAP_NET_ADMIN + CAP_NET_RAW) and the nftables rules that splitter prints (G8 in `internal/pc/PLAN.md`). |
| `lua/zapret-lib.lua`, `lua/zapret-antidpi.lua`, `lua/zapret-auto.lua` | winws2 and nfqws2 | all | the same zapret2 zip (`lua/`) | zapret2 strategies are Lua; the engine loads these with `--lua-init`. They go into a `lua/` folder next to the engine binary. |

Download only from those two official release pages. Never use mirrors, forks or "repacks".

## 2. Where the binaries live

Three different stages. Only stage A is needed now.

| Stage | Location | Who puts them there | Status |
|---|---|---|---|
| **A. Development** (tests, `sing-box check`, manual runs) | `third_party/bin/<os>-<arch>/` (git-ignored) | a fetch script that downloads by manifest and checks sha256, or you by hand | **do now** |
| **B. Runtime lookup** | `Binary` paths in `app.yaml`; if empty, a default folder (decision D1 below) | the app reads it; it never downloads anything | decided during M9/M10 |
| **C. End-user install** | an installer (built by the Tauri bundler, `docs/adr/0003-desktop-ui-and-packaging.md`) that bundles the pinned binaries next to `splitter` | a release pipeline | later (packaging) |

Development layout after stage A:

```
third_party/bin/
  windows-amd64/
    sing-box.exe
    libcronet.dll
    winws2.exe
    WinDivert.dll
    WinDivert64.sys
    cygwin1.dll
    lua/zapret-lib.lua, zapret-antidpi.lua, zapret-auto.lua
    LICENSE.sing-box, LICENSE.zapret2
  linux-amd64/   (same for linux-arm64)
    sing-box
    libcronet.so
    nfqws2
    lua/...
    LICENSE.sing-box, LICENSE.zapret2
```

**Status (2026-10-01):** done for sing-box 1.14.2 and zapret2 1.0.5.2 (steps 1–4, by hand; all hashes verified and recorded in the manifest). The fetch tool exists (`go run ./scripts/fetch`). Remaining: the version check below, steps 5–6 when you test for real, and the decisions in §4.

## 3. Steps

### Step 1 — choose versions

- [ ] **sing-box**: take the newest **stable** release (not alpha/beta/rc). The config format changed between minor versions (rule actions in 1.11, the DNS server format in 1.12), and the generator is written against exactly one version. Write the version down.
- [ ] **zapret2**: take the newest release. winws2 and nfqws2 come from the same archive, so they share one version.
- [ ] Check that the zapret2 archive actually contains `binaries/windows-x86_64/winws2.exe` (with its DLL/SYS files), `binaries/linux-x86_64/nfqws2` / `linux-arm64/nfqws2` and `lua/`. Folder names change between releases sometimes; write down the exact paths inside the archive.

### Step 2 — download and hash

- [ ] Download each archive (one sing-box archive per platform, one zapret2 archive) from the official release page.
- [ ] Compute the sha256 of each **archive** and compare it with the digest shown on the GitHub release page, where one is shown:

  Windows (PowerShell):
  ```powershell
  Get-FileHash .\sing-box-<ver>-windows-amd64.zip -Algorithm SHA256
  ```
  Linux:
  ```bash
  sha256sum sing-box-<ver>-linux-amd64.tar.gz
  ```
- [ ] Also compute the sha256 of each **extracted file** you will use (`sing-box.exe`, `winws2.exe`, `WinDivert.dll`, `WinDivert64.sys`, `cygwin1.dll`, `nfqws2`, the `lua/` files). The fetch script checks both the archive and the files.

### Step 3 — fill `third_party/manifest.yaml`

The current format has one `url` + `sha256` per platform. Archives contain several files, so add a `files` list per platform: the path inside the archive, the target name, and the file's sha256. Example (placeholders, not real values):

```yaml
binaries:
  - name: sing-box
    version: "<x.y.z>"
    license: GPL-3.0-or-later
    source: https://github.com/SagerNet/sing-box/releases
    platforms:
      windows-amd64:
        url: <release asset URL>
        sha256: <archive sha256>
        files:
          - from: sing-box-<x.y.z>-windows-amd64/sing-box.exe
            to: sing-box.exe
            sha256: <file sha256>
      linux-amd64:
        url: <release asset URL>
        sha256: <archive sha256>
        files:
          - from: sing-box-<x.y.z>-linux-amd64/sing-box
            to: sing-box
            sha256: <file sha256>
      linux-arm64: { ... }

  - name: zapret2
    version: "<vNN.N>"
    license: <see archive>
    source: https://github.com/bol-van/zapret2/releases
    platforms:
      windows-amd64:
        url: <release asset URL>
        sha256: <archive sha256>
        files:
          - { from: <path>/winws2.exe,      to: winws2.exe,       sha256: <...> }
          - { from: <path>/WinDivert.dll,   to: WinDivert.dll,   sha256: <...> }
          - { from: <path>/WinDivert64.sys, to: WinDivert64.sys, sha256: <...> }
          - { from: <path>/cygwin1.dll,     to: cygwin1.dll,     sha256: <...> }
      linux-amd64:
        url: <same zapret2 archive URL>
        sha256: <archive sha256>
        files:
          - { from: <path>/nfqws2, to: nfqws2, sha256: <...> }
      linux-arm64: { ... }
```

- [ ] Fill in the manifest and commit it (the manifest only, never the binaries).
- [ ] Tell the agent the versions. It will record them in the `API.md` headers of `singbox`, `winws` and `nfqws` ("verified against sing-box x.y.z / zapret2 vNN").

### Step 4 — get the files into `third_party/bin/`

The fetch tool (`scripts/fetch`) does this. It reads the manifest, downloads each archive over HTTPS, checks the archive sha256, extracts only the listed files, checks each file's sha256, and installs them into `third_party/bin/<os>-<arch>/`. A mismatch stops it and leaves nothing half-installed. Archives are cached (in your user cache folder) by their sha256, so a re-run needs no download. Files that are already correct are skipped.

- [ ] After a fresh clone, or after changing the manifest (this uses the network):
  ```bash
  go run ./scripts/fetch
  ```
  It installs this machine's platform by default. Other options: `-platform all` (every platform in the manifest), `-platform linux-amd64,linux-arm64`, `-only sing-box`. `make fetch` does the same.
- [ ] To check the installed files at any time (no network):
  ```bash
  go run ./scripts/fetch -check -platform all
  ```

Then check that they start (no admin needed for these):

```powershell
.\third_party\bin\windows-amd64\sing-box.exe version
```
```powershell
.\third_party\bin\windows-amd64\winws2.exe --help
```

### Step 5 — prepare the Windows dev machine

- [ ] Running `splitter run` for real needs an **elevated** terminal (TUN + WinDivert). Tests do not.
- [ ] Stop other tools that use WinDivert or a TUN before testing: GoodbyeDPI, another zapret / zapret2, other VPN clients, another sing-box/Clash. Two WinDivert users, or two TUNs, on one machine give confusing results.
- [ ] Antivirus software sometimes flags WinDivert/winws as a "hack tool". If yours quarantines a file, the sha256 check fails. Decide for yourself whether to add an exception for `third_party/bin/`; the agent will not do it.

### Step 6 — prepare a Linux dev machine (only if Linux desktop is tested)

- [ ] `nft` installed, and the `nfnetlink_queue` kernel module available.
- [ ] Run `splitter run` as root. When it refuses and prints the nftables `Setup` commands, review them and apply them yourself. Remove them later with the printed `Teardown` command.

### Step 7 — updating later

- [ ] Change the version, URLs and hashes in the manifest, then re-run the fetch.
- [ ] Ask the agent to re-verify the generator against the new version (golden files + `sing-box check`). Do not bump sing-box casually; config breaking changes are common between minor versions.

## 4. Decisions you need to make

| # | Question | Options | Suggestion |
|---|---|---|---|
| D1 | Where does the app look for binaries when `app.yaml` gives no path? | (a) next to `splitter` (`<exe dir>/`); (b) `Paths.Data/bin`; (c) only explicit paths | (a): simplest for a zip/installer, and the same as how zapret ships winws |
| D2 | Ship the binaries with the app (stage C) or ask users to install them? | bundle / user-installed | bundle the pinned versions; user-installed versions break the "verified against x.y.z" guarantee |
| D3 | Should the app check the sha256 of the binaries at every start? | yes / no | yes, cheap; protects against a swapped or quarantined-then-restored file. Needs the hashes embedded at build time |
| D4 | Licenses for bundling (stage C) | – | sing-box is GPL-3.0 (with an extra condition on using its name), WinDivert is LGPL-3/GPL-2, cygwin1.dll is LGPL, zapret2 is MIT. Check what bundling requires (license texts, source offers) before the first public release |
| D5 | Should CI download the pinned binaries for an integration job (`sing-box check` on every golden)? | yes / no | yes, in a separate job; `go test ./...` stays offline as `AGENTS.md` requires |

## 5. What the agent does after this

- Records the pinned versions in `singbox`, `winws`, `nfqws` `API.md` and checks flag/field names against those versions.
- Adds the `//go:build integration` test that runs `sing-box check` with `third_party/bin/<os>-<arch>/sing-box`, skipping when the file is missing.
- Implements D1–D3 in `internal/pc` (M9/M10) once you decide.
