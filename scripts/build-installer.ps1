# Builds the Windows installer: dist\dev-setup.exe (double-click to install dev for this user).
# The installer is dev itself: started under the name dev-setup.exe it copies itself to
# %LOCALAPPDATA%\dev\bin, adds that folder to your PATH and sets up PowerShell and Git Bash.
#
#   powershell -ExecutionPolicy Bypass -File scripts\build-installer.ps1
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
if (-not (Get-Command cargo -ErrorAction SilentlyContinue)) {
    $env:PATH = "$env:USERPROFILE\.cargo\bin;$env:PATH"
}
cargo build --release
if ($LASTEXITCODE -ne 0) { throw "cargo build failed" }
New-Item -ItemType Directory -Force dist | Out-Null
Copy-Item target\release\dev.exe dist\dev-setup.exe -Force
Copy-Item target\release\dev.exe dist\dev.exe -Force
Copy-Item install.sh dist\install.sh -Force
$h = (Get-FileHash dist\dev-setup.exe -Algorithm SHA256).Hash.ToLower()
"$h  dev-setup.exe" | Set-Content -Encoding ascii dist\dev-setup.exe.sha256
Write-Host "Built dist\dev-setup.exe (sha256 $h)"
