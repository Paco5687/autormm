<#
.SYNOPSIS
  autormm one-line bootstrap installer for Windows (agent or client).

.DESCRIPTION
  Downloads the latest Windows release archive, verifies its checksum, and runs
  the requested piece's installer. Uses gh if authenticated (also works for a private fork);
  falls back to the GitHub API if gh is unavailable.

.EXAMPLE
  irm <url>/deploy/get.ps1 | iex; Install-Autormm agent -Server https://... -Token ENROLL
  # or:  .\get.ps1 -Piece agent -Server http://HUB-IP:8765 -Token ENROLL
#>
param(
  [Parameter(Mandatory = $true)][ValidateSet('agent','client')][string]$Piece,
  [string]$Server = "",
  [string]$Token = "",
  [string]$Tags = "",
  [string]$Version = ""
)

$ErrorActionPreference = "Stop"
$repo = if ($env:AUTORMM_REPO) { $env:AUTORMM_REPO } else { "Paco5687/autormm" }
$arch = "amd64"  # Windows release ships amd64
$pattern = "autormm_*_windows_$arch.zip"
$tmp = Join-Path $env:TEMP ("autormm-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

Write-Host "Fetching autormm (windows/$arch)…"
if (Get-Command gh -ErrorAction SilentlyContinue) {
  $ghArgs = @("release","download","--repo",$repo,"--dir",$tmp,"--pattern",$pattern,"--pattern","SHA256SUMS","--clobber")
  if ($Version) { $ghArgs += $Version }
  gh @ghArgs
} else {
  $api = if ($Version) { "https://api.github.com/repos/$repo/releases/tags/$Version" }
         else { "https://api.github.com/repos/$repo/releases/latest" }
  $headers = @{}
  if ($env:GITHUB_TOKEN) { $headers["Authorization"] = "Bearer $env:GITHUB_TOKEN" }
  $rel = Invoke-RestMethod -Uri $api -Headers $headers
  foreach ($a in $rel.assets) {
    if ($a.name -like $pattern -or $a.name -eq "SHA256SUMS") {
      Invoke-WebRequest -Uri $a.browser_download_url -Headers $headers -OutFile (Join-Path $tmp $a.name)
    }
  }
}

$archive = Get-ChildItem -Path $tmp -Filter "autormm_*_windows_$arch.zip" | Select-Object -First 1
if (-not $archive) { throw "no matching release asset found" }

$sumsFile = Join-Path $tmp "SHA256SUMS"
if (Test-Path $sumsFile) {
  Write-Host "Verifying checksum…"
  $want = (Select-String -Path $sumsFile -Pattern ([regex]::Escape($archive.Name))).Line.Split(' ')[0]
  $got  = (Get-FileHash -Algorithm SHA256 $archive.FullName).Hash.ToLower()
  if ($want -ne $got) { throw "checksum verification FAILED" }
} else {
  Write-Warning "SHA256SUMS not found, skipping checksum verification"
}

Expand-Archive -Path $archive.FullName -DestinationPath $tmp -Force
Push-Location $tmp
try {
  switch ($Piece) {
    'agent' {
      & powershell -ExecutionPolicy Bypass -File .\deploy\install-agent.ps1 `
        -Server $Server -Token $Token -Tags $Tags -Bin .\autormm-agent.exe
    }
    'client' {
      New-Item -ItemType Directory -Force -Path (Join-Path $env:LOCALAPPDATA "autormm") | Out-Null
      $dest = Join-Path $env:LOCALAPPDATA "autormm\autormm-client.exe"
      Copy-Item -Force .\autormm-client.exe $dest
      Write-Host "Installed autormm-client to $dest"
      if ($Server -and $Token) { & $dest login --server $Server --token $Token }
      else { Write-Host "Run 'autormm-client login' to configure." }
    }
  }
} finally { Pop-Location }
