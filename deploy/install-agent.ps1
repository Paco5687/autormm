<#
.SYNOPSIS
  Install the autormm agent on Windows as a per-user logon task.

.DESCRIPTION
  Screen capture and input injection require the interactive desktop session, so
  the agent runs as a Scheduled Task at logon (session 1+), NOT as a session-0
  service. Run this in a normal (non-elevated is fine) PowerShell as the user who
  will be at the console.

.EXAMPLE
  .\install-agent.ps1 -Server http://HUB-IP:8765 -Token ENROLL -Tags "desktop,office"
#>
param(
  [Parameter(Mandatory = $true)][string]$Server,
  [Parameter(Mandatory = $true)][string]$Token,
  [string]$Id = "",
  [string]$Tags = "",
  [switch]$Insecure,
  [string]$Bin = "$PSScriptRoot\autormm-agent-windows-amd64.exe"
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path $Bin)) { throw "agent binary not found: $Bin" }

$installDir = Join-Path $env:LOCALAPPDATA "autormm"
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$dest = Join-Path $installDir "autormm-agent.exe"
Copy-Item -Force $Bin $dest

$argList = @("-server", $Server, "-token", $Token)
if ($Id)   { $argList += @("-id", $Id) }
if ($Tags) { $argList += @("-tags", $Tags) }
if ($Insecure) { $argList += @("-insecure") }

$action  = New-ScheduledTaskAction -Execute $dest -Argument ($argList -join " ")
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
             -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Highest

Register-ScheduledTask -TaskName "autormm-agent" -Action $action -Trigger $trigger `
  -Settings $settings -Principal $principal -Force | Out-Null

Start-ScheduledTask -TaskName "autormm-agent"
Write-Host "Installed autormm-agent (Scheduled Task 'autormm-agent'). It will start now and at each logon."
