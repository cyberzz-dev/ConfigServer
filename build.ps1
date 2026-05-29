# build.ps1 — Windows build script for ConfigServer
# Usage:
#   .\build.ps1                                   # build everything (WebUI + all Windows binaries)
#   .\build.ps1 -Target allinone                  # build WebUI + allinone (Windows)
#   .\build.ps1 -Target allinone -CrossCompile    # build WebUI + allinone for Linux/amd64
#   .\build.ps1 -Target configserver              # build configserver (no WebUI needed)
#   .\build.ps1 -Target admin                     # build WebUI + admin
#   .\build.ps1 -Target webui                     # build WebUI only
#   .\build.ps1 -SkipWebUI                        # skip npm build (use existing dist/)
#   .\build.ps1 -CrossCompile                     # cross-compile all binaries for Linux/amd64
#   .\build.ps1 -Version 1.0.0                    # embed version string

param(
    [ValidateSet("all", "allinone", "configserver", "admin", "webui")]
    [string]$Target = "all",
    [string]$Version = "dev",
    [switch]$SkipWebUI,
    [switch]$CrossCompile
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

# ── webui ────────────────────────────────────────────────────────────────────

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

# ── go binaries ───────────────────────────────────────────────────────────────

function Build-Go([string]$cmdName, [string]$exeName) {
    Write-Step "Building $cmdName"
    $env:CGO_ENABLED = "0"
    $env:GOOS        = "windows"
    $env:GOARCH      = "amd64"
    Invoke-Cmd go @(
        "build",
        "-trimpath",
        "-ldflags", "-s -w -X main.Version=$Version",
        "-o", "$ScriptDir\bin\$exeName",
        "./cmd/$cmdName/"
    )
    Write-Ok "bin\$exeName"
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

# ── linux cross-compile (optional helper) ─────────────────────────────────────

function Build-GoLinux([string]$cmdName, [string]$binName) {
    Write-Step "Cross-compiling $cmdName for Linux/amd64"
    $env:CGO_ENABLED = "0"
    $env:GOOS        = "linux"
    $env:GOARCH      = "amd64"
    Invoke-Cmd go @(
        "build",
        "-trimpath",
        "-ldflags", "-s -w -X main.Version=$Version",
        "-o", "$ScriptDir\bin\linux_$binName",
        "./cmd/$cmdName/"
    )
    Write-Ok "bin\linux_$binName"
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

# ── main ──────────────────────────────────────────────────────────────────────

New-Item -ItemType Directory -Force -Path "$ScriptDir\bin" | Out-Null
Push-Location $ScriptDir

try {
    $needWebUI = (-not $SkipWebUI) -and ($Target -in @("all", "allinone", "admin"))

    if ($needWebUI) { Build-WebUI }

    switch ($Target) {
        "webui" { <# already done above #> }
        "allinone" {
            if ($CrossCompile) { Build-GoLinux "allinone" "allinone" }
            else               { Build-Go     "allinone" "allinone.exe" }
        }
        "configserver" {
            if ($CrossCompile) { Build-GoLinux "configserver" "configserver" }
            else               { Build-Go     "configserver" "configserver.exe" }
        }
        "admin" {
            if ($CrossCompile) { Build-GoLinux "admin" "admin" }
            else               { Build-Go     "admin" "admin.exe" }
        }
        "all" {
            if ($CrossCompile) {
                Build-GoLinux "allinone"    "allinone"
                Build-GoLinux "configserver" "configserver"
                Build-GoLinux "admin"       "admin"
            } else {
                Build-Go "allinone"    "allinone.exe"
                Build-Go "configserver" "configserver.exe"
                Build-Go "admin"       "admin.exe"
            }
        }
    }

    Write-Host ""
    Write-Host "Build complete. Output: $ScriptDir\bin\" -ForegroundColor Green
    Get-ChildItem "$ScriptDir\bin\" | Format-Table Name, @{N='Size(KB)';E={[math]::Round($_.Length/1KB,1)}}
} finally {
    Pop-Location
}
