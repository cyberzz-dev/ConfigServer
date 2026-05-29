# build-linux.ps1 — Cross-compile ConfigServer for Linux
# Usage:
#   .\build-linux.ps1                             # build all binaries for linux/amd64
#   .\build-linux.ps1 amd64                       # build all binaries for linux/amd64
#   .\build-linux.ps1 arm64                       # build all binaries for linux/arm64
#   .\build-linux.ps1 amd64 -Target allinone      # build only allinone for linux/amd64
#   .\build-linux.ps1 amd64 -SkipWebUI            # skip npm build (use existing dist/)
#   .\build-linux.ps1 amd64 -Version 1.0.0        # embed version string

param(
    [string]$Arch = "amd64",
    [ValidateSet("all", "allinone", "configserver", "admin")]
    [string]$Target = "all",
    [string]$Version = "dev",
    [switch]$SkipWebUI
)

$ErrorActionPreference = "Stop"
$ScriptDir = $PSScriptRoot

# ── helpers ──────────────────────────────────────────────────────────────────

function Write-Step([string]$msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Ok([string]$msg)   { Write-Host "    $msg" -ForegroundColor Green }
function Write-Fail([string]$msg) { Write-Host "    ERROR: $msg" -ForegroundColor Red ; exit 1 }

function Invoke-Cmd([string]$prog, [string[]]$cmdArgs) {
    & $prog @cmdArgs
    if ($LASTEXITCODE -ne 0) { Write-Fail "'$prog $($cmdArgs -join ' ')' exited with code $LASTEXITCODE" }
}

# ── validate arch ─────────────────────────────────────────────────────────────

$validArchs = @("amd64", "arm64", "386", "arm")
if ($Arch -notin $validArchs) {
    Write-Fail "Unsupported arch '$Arch'. Valid values: $($validArchs -join ', ')"
}

# ── webui ─────────────────────────────────────────────────────────────────────

function Build-WebUI {
    Write-Step "Building React WebUI"
    if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
        Write-Fail "npm not found. Install Node.js >= 18 and re-run."
    }
    Push-Location "$ScriptDir\webui"
    try {
        Invoke-Cmd npm @("install", "--prefer-offline")
        Invoke-Cmd npm @("run", "build")
    } finally {
        Pop-Location
    }
    Write-Ok "WebUI -> webui/dist/"
}

# ── linux cross-compile ───────────────────────────────────────────────────────

function Build-Linux([string]$cmdName) {
    $suffix = if ($Arch -eq "amd64") { "" } else { "_$Arch" }
    $binName = "linux${suffix}_${cmdName}"
    Write-Step "Cross-compiling $cmdName for linux/$Arch"
    $env:CGO_ENABLED = "0"
    $env:GOOS        = "linux"
    $env:GOARCH      = $Arch
    Invoke-Cmd go @(
        "build",
        "-trimpath",
        "-ldflags", "-s -w -X main.Version=$Version",
        "-o", "$ScriptDir\bin\$binName",
        "./cmd/$cmdName/"
    )
    Write-Ok "bin\$binName"
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

# ── main ──────────────────────────────────────────────────────────────────────

New-Item -ItemType Directory -Force -Path "$ScriptDir\bin" | Out-Null
Push-Location $ScriptDir

try {
    $needWebUI = (-not $SkipWebUI) -and ($Target -in @("all", "allinone", "admin"))
    if ($needWebUI) { Build-WebUI }

    switch ($Target) {
        "allinone"    { Build-Linux "allinone" }
        "configserver"{ Build-Linux "configserver" }
        "admin"       { Build-Linux "admin" }
        "all" {
            Build-Linux "allinone"
            Build-Linux "configserver"
            Build-Linux "admin"
        }
    }

    Write-Host ""
    Write-Host "Build complete. Output: $ScriptDir\bin\" -ForegroundColor Green
    Get-ChildItem "$ScriptDir\bin\" | Where-Object { $_.Name -like "linux*" } |
        Format-Table Name, @{N='Size(KB)';E={[math]::Round($_.Length/1KB,1)}}
} finally {
    Pop-Location
}
