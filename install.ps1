<#
.SYNOPSIS
  Hydra (hyctl), standalone binary installer for Windows.

.DESCRIPTION
  Downloads the prebuilt hyctl.exe for your architecture from the latest GitHub
  release, verifies it against the published checksums, and installs it.
  No Go toolchain required, and no administrator rights.

  The PowerShell sibling of install.sh, which handles macOS and Linux only:
  it downloads a .tar.gz unconditionally, while Windows archives are .zip, so
  it refuses to run here rather than handing a zip to tar (#264).

.PARAMETER Version
  Pin a specific release, e.g. v1.2.0. Defaults to the latest.
  Also settable as $env:HYDRA_VERSION.

.PARAMETER BinDir
  Install directory. Defaults to $env:LOCALAPPDATA\Programs\hyctl, a
  per-user location, so no elevation is needed.
  Also settable as $env:HYDRA_BIN.

.EXAMPLE
  irm https://raw.githubusercontent.com/ankit373/hydra/main/install.ps1 | iex

.EXAMPLE
  $env:HYDRA_VERSION = 'v1.2.0'
  irm https://raw.githubusercontent.com/ankit373/hydra/main/install.ps1 | iex
#>

[CmdletBinding()]
param(
    [string]$Version = $env:HYDRA_VERSION,
    [string]$BinDir  = $env:HYDRA_BIN
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Repo    = 'ankit373/hydra'
$Project = 'hydra'      # goreleaser project_name → archive filename prefix
$Binary  = 'hyctl.exe'  # binary name inside the archive
$Base    = "https://github.com/$Repo/releases"

function Write-Info { param([string]$Message) Write-Host "  $Message" }
function Write-Warn { param([string]$Message) Write-Warning $Message }
function Stop-WithError {
    param([string]$Message)
    Write-Host "  x $Message" -ForegroundColor Red
    exit 1
}

# Windows PowerShell 5.1 still negotiates TLS 1.0 by default, which GitHub
# refuses. Without this the very first request fails with an unhelpful
# "underlying connection was closed".
try {
    [Net.ServicePointManager]::SecurityProtocol =
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
    # PowerShell 7+ manages this itself and may not expose the setter.
    Write-Verbose "could not set TLS 1.2 explicitly: $($_.Exception.Message)"
}

Write-Host 'Hydra installer (hyctl)'

# ── Detect architecture ───────────────────────────────────────────────────────
# PROCESSOR_ARCHITECTURE reports the *process* architecture. A 32-bit
# PowerShell on 64-bit Windows reports x86 and sets PROCESSOR_ARCHITEW6432 to
# the real one, so that takes precedence, otherwise a perfectly capable
# machine would be told it is unsupported.
$rawArch = $env:PROCESSOR_ARCHITEW6432
if ([string]::IsNullOrEmpty($rawArch)) { $rawArch = $env:PROCESSOR_ARCHITECTURE }

switch ($rawArch) {
    'AMD64' { $Arch = 'amd64' }
    'x86_64' { $Arch = 'amd64' }
    'ARM64' { $Arch = 'arm64' }
    default {
        Stop-WithError "unsupported architecture: $rawArch (hyctl ships windows/amd64 and windows/arm64)"
    }
}

# ── Resolve version ───────────────────────────────────────────────────────────
if ([string]::IsNullOrEmpty($Version)) {
    try {
        $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" `
            -Headers @{ 'User-Agent' = 'hyctl-installer' }
        $Tag = $release.tag_name
    } catch {
        Stop-WithError "could not determine latest release, set `$env:HYDRA_VERSION=vX.Y.Z ($($_.Exception.Message))"
    }
} else {
    $Tag = $Version
}
if ([string]::IsNullOrEmpty($Tag)) {
    Stop-WithError 'could not determine a release tag'
}

# Archive version = tag without a leading 'v', matching goreleaser's
# name_template. Kept identical to install.sh, npm and pip; the contract test
# in cmd/hydra/dist_naming_test.go is what keeps all five in step.
$Ver     = $Tag -replace '^v', ''
$Archive = "${Project}_${Ver}_windows_${Arch}.zip"
$Url     = "$Base/download/$Tag/$Archive"
$SumsUrl = "$Base/download/$Tag/checksums.txt"

Write-Info "Target  : windows/$Arch"
Write-Info "Archive : $Archive"
Write-Info "Release : $Tag"

# ── Download ──────────────────────────────────────────────────────────────────
$Tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("hyctl-" + [System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $Tmp -Force | Out-Null

try {
    $ArchivePath = Join-Path $Tmp $Archive
    try {
        # A progress bar makes Invoke-WebRequest an order of magnitude slower.
        $prevProgress = $ProgressPreference
        $ProgressPreference = 'SilentlyContinue'
        Invoke-WebRequest -Uri $Url -OutFile $ArchivePath -UseBasicParsing
        $ProgressPreference = $prevProgress
    } catch {
        Stop-WithError "download failed: $Url ($($_.Exception.Message))"
    }

    # ── Verify ────────────────────────────────────────────────────────────────
    # Same three-way policy as install.sh, npm and pip, and for the same reason:
    #   unreachable checksums.txt  → warn and continue; a network blip should
    #                                not brick an install
    #   present but not listing us → FAIL CLOSED; the realistic cause is naming
    #                                drift, and silently skipping verification
    #                                for every user is #241
    #   listed and mismatched      → FAIL
    $sums = $null
    try {
        $sums = (Invoke-WebRequest -Uri $SumsUrl -UseBasicParsing).Content
    } catch {
        Write-Warn "checksums.txt not published for $Tag, skipping verification"
    }

    if ($null -ne $sums) {
        $line = $sums -split "`n" | Where-Object { $_.Trim().EndsWith($Archive) } | Select-Object -First 1
        if (-not $line) {
            Stop-WithError "$Archive is not listed in checksums.txt, refusing to install unverified"
        }
        $expected = ($line.Trim() -split '\s+')[0]
        $actual = (Get-FileHash -Path $ArchivePath -Algorithm SHA256).Hash.ToLower()
        if ($expected.ToLower() -ne $actual) {
            Stop-WithError "checksum mismatch, refusing to install (expected $expected, got $actual)"
        }
        Write-Info 'Checksum: verified'
    }

    # ── Extract ───────────────────────────────────────────────────────────────
    $ExtractDir = Join-Path $Tmp 'x'
    Expand-Archive -Path $ArchivePath -DestinationPath $ExtractDir -Force
    $Extracted = Join-Path $ExtractDir $Binary
    if (-not (Test-Path $Extracted)) {
        Stop-WithError "archive did not contain '$Binary'"
    }

    # ── Choose install dir ────────────────────────────────────────────────────
    # Per-user by default so no elevation is required. Program Files would need
    # admin, and an installer that demands it for a CLI is a worse default than
    # one that asks the user to extend PATH.
    if ([string]::IsNullOrEmpty($BinDir)) {
        $BinDir = Join-Path $env:LOCALAPPDATA 'Programs\hyctl'
    }
    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null

    $Dest = Join-Path $BinDir $Binary
    Copy-Item -Path $Extracted -Destination $Dest -Force
    Write-Info "Installed: $Dest"

    # ── PATH ──────────────────────────────────────────────────────────────────
    # The user's PATH is persistent state, so it is only extended when hyctl's
    # directory is genuinely absent, and only at User scope, never Machine,
    # which would need elevation and affect everyone on the box.
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($null -eq $userPath) { $userPath = '' }
    $onPath = ($userPath -split ';' | Where-Object { $_.TrimEnd('\') -ieq $BinDir.TrimEnd('\') }).Count -gt 0

    if (-not $onPath) {
        try {
            $newPath = if ([string]::IsNullOrEmpty($userPath)) { $BinDir } else { "$userPath;$BinDir" }
            [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
            $env:Path = "$env:Path;$BinDir"
            Write-Info "Added to your user PATH: $BinDir"
            Write-Warn 'Open a new terminal for the PATH change to take effect.'
        } catch {
            Write-Warn "could not update PATH automatically, add it yourself: $BinDir"
        }
    }

    Write-Host ''
    Write-Host "Done, hyctl $Tag installed. Run:  hyctl init"
} finally {
    Remove-Item -Path $Tmp -Recurse -Force -ErrorAction SilentlyContinue
}
