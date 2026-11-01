<#
.SYNOPSIS
  Builds the Windows app folder dist/app: splitter.exe, the Tauri app (Obhod.exe) and the pinned
  third-party programs next to them. For humans (make app); it needs no administrator rights.

.DESCRIPTION
  1. go build splitter.exe (cmd/splitter) into the output folder.
  2. npm ci + the Tauri release build of ui/ (no installer bundle), copied as Obhod.exe.
  3. Copies every file listed in scripts/app-files.txt from third_party/bin/windows-amd64.
  It stops with an error as soon as a file is missing; it never leaves a half-filled folder looking fine.
  Run "go run ./scripts/fetch -platform windows-amd64" first to download the pinned programs.

.PARAMETER Out
  Output folder (default dist/app). It is emptied first.

.PARAMETER SkipBuild
  Do not build splitter.exe and the UI; only assemble and check the pinned files (used to test the script).
#>
[CmdletBinding()]
param(
  [string]$Out = "dist/app",
  [switch]$SkipBuild
)
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

function Fail([string]$msg) {
  Write-Error "build-app: $msg"
  exit 1
}

$listFile = Join-Path $root "scripts/app-files.txt"
$binDir = Join-Path $root "third_party/bin/windows-amd64"
if (-not (Test-Path $listFile)) { Fail "scripts/app-files.txt is missing" }

$files = @(Get-Content $listFile | ForEach-Object { ($_ -replace '#.*$', '').Trim() } | Where-Object { $_ -ne "" })
if ($files.Count -eq 0) { Fail "scripts/app-files.txt lists no files" }

# Check the pinned files before the long builds.
$missing = @($files | Where-Object { -not (Test-Path -PathType Leaf (Join-Path $binDir $_)) })
if ($missing.Count -gt 0) {
  Fail ("missing in third_party/bin/windows-amd64: " + ($missing -join ", ") + ". Run: go run ./scripts/fetch -platform windows-amd64")
}

if (Test-Path $Out) { Remove-Item -Recurse -Force $Out }
New-Item -ItemType Directory -Force $Out | Out-Null
$outAbs = (Resolve-Path $Out).Path

if (-not $SkipBuild) {
  $module = (& go list -m).Trim()
  $version = "dev"; $commit = ""
  try { $version = (& git describe --tags --always --dirty 2>$null).Trim() } catch {}
  try { $commit = (& git rev-parse --short HEAD 2>$null).Trim() } catch {}
  $ld = "-X $module/internal/lib/version.Version=$version -X $module/internal/lib/version.Commit=$commit"
  & go build -ldflags $ld -o (Join-Path $outAbs "splitter.exe") ./cmd/splitter
  if ($LASTEXITCODE -ne 0) { Fail "go build splitter.exe failed" }

  Push-Location (Join-Path $root "ui")
  try {
    & npm ci
    if ($LASTEXITCODE -ne 0) { Fail "npm ci failed" }
    & npm run tauri:build -- --no-bundle
    if ($LASTEXITCODE -ne 0) { Fail "the Tauri build failed" }
  } finally { Pop-Location }
  $app = Join-Path $root "ui/src-tauri/target/release/obhod-ui.exe"
  if (-not (Test-Path $app)) { Fail "the Tauri build did not produce $app" }
  Copy-Item $app (Join-Path $outAbs "Obhod.exe")
}

foreach ($f in $files) {
  $dst = Join-Path $outAbs $f
  New-Item -ItemType Directory -Force (Split-Path -Parent $dst) | Out-Null
  Copy-Item (Join-Path $binDir $f) $dst
}

# Final check of the folder itself.
$expected = @($files)
if (-not $SkipBuild) { $expected += @("splitter.exe", "Obhod.exe") }
$absent = @($expected | Where-Object { -not (Test-Path -PathType Leaf (Join-Path $outAbs $_)) })
if ($absent.Count -gt 0) { Fail ("the app folder is incomplete, missing: " + ($absent -join ", ")) }
Write-Host "build-app: $outAbs is ready ($($expected.Count) files)"
