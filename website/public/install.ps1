# install.ps1 — Install the rat binary on Windows.
# Usage: irm https://runanything.dev/install.ps1 | iex
#
# Installs to $env:LOCALAPPDATA\Programs\rat\bin by default.

$ErrorActionPreference = "Stop"

$Repo = "maximerivest/rat"
$InstallDir = if ($env:RAT_INSTALL_DIR) { $env:RAT_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\rat\bin" }
$BaseUrl = "https://github.com/$Repo/releases/latest/download"

# Detect architecture.
switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
  "X64"   { $Arch = "amd64" }
  "Arm64" { $Arch = "arm64" }
  default { throw "Unsupported architecture: $([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)" }
}

$Candidates = @(
  "rat-windows-$Arch.exe",
  "rat-windows-$Arch"
)

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

$Dest = Join-Path $InstallDir "rat.exe"
$Tmp = Join-Path $InstallDir "rat.tmp.$PID.exe"

try {
  $Downloaded = $false
  $Errors = @()

  foreach ($Binary in $Candidates) {
    $Url = "$BaseUrl/$Binary"
    Write-Host "Downloading rat from $Url..."
    try {
      Invoke-WebRequest -Uri $Url -OutFile $Tmp -UseBasicParsing
      $Downloaded = $true
      break
    } catch {
      $Errors += "${Binary}: $($_.Exception.Message)"
      Remove-Item -Force -ErrorAction SilentlyContinue $Tmp
    }
  }

  if (-not $Downloaded) {
    throw "Could not download rat. Tried: $($Errors -join '; ')"
  }

  Move-Item -Force $Tmp $Dest

  # Add install dir to the user PATH if needed.
  $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
  $PathParts = @()
  if ($UserPath) { $PathParts = $UserPath -split ";" }
  $AlreadyOnPath = $PathParts | Where-Object { $_ -and ([IO.Path]::GetFullPath($_).TrimEnd('\') -ieq [IO.Path]::GetFullPath($InstallDir).TrimEnd('\')) }

  if (-not $AlreadyOnPath) {
    $NewPath = if ($UserPath) { "$UserPath;$InstallDir" } else { $InstallDir }
    [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
    $env:Path = "$env:Path;$InstallDir"
    Write-Host "Added $InstallDir to your user PATH. Restart terminals to pick it up."
  }

  Write-Host ""
  Write-Host "Installed rat to $Dest"
  Write-Host ""
  Write-Host "Get started:"
  Write-Host "  rat install py"
  Write-Host "  rat py"
} finally {
  Remove-Item -Force -ErrorAction SilentlyContinue $Tmp
}
