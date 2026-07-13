<#
.SYNOPSIS
  Install the autormm desktop agent on Windows as a system-tray app.

.DESCRIPTION
  The agent runs as a normal user-session GUI app (which is what screen capture
  needs) and shows a tray status icon. It starts at logon via a per-user
  registry Run key that the app registers itself — no admin, no scheduled task.
  Run this as the user who will be at the console.

.EXAMPLE
  .\install-agent.ps1 -Server http://HUB-IP:8765 -Token ENROLL -Tags "desktop,office"
#>
param(
  [Parameter(Mandatory = $true)][string]$Server,
  [Parameter(Mandatory = $true)][string]$Token,
  [string]$Id = "",
  [string]$Tags = "",
  [switch]$Insecure,
  [string]$Bin = "$PSScriptRoot\autormm-agent-tray.exe"
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path $Bin)) { throw "agent binary not found: $Bin" }

$installDir = Join-Path $env:LOCALAPPDATA "autormm"
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$dest = Join-Path $installDir "autormm-agent-tray.exe"

# Stop any previous instance so the exe isn't locked while we overwrite it.
Get-Process autormm-agent-tray -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 300
Copy-Item -Force $Bin $dest

$argList = @("-server", $Server, "-token", $Token)
if ($Id)   { $argList += @("-id", $Id) }
if ($Tags) { $argList += @("-tags", $Tags) }
if ($Insecure) { $argList += @("-insecure") }

# Launch it; the app registers its own logon autostart (HKCU\...\Run).
Start-Process -FilePath $dest -ArgumentList ($argList -join " ")
Write-Host "Installed autormm agent. A tray icon will appear, and it will start automatically at logon."
