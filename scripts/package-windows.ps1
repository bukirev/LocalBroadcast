param([string]$Version = '0.2.1-beta')
$ErrorActionPreference = 'Stop'
$project = Split-Path $PSScriptRoot -Parent
$stage = Join-Path $project ('dist\package-' + $Version)
New-Item -ItemType Directory -Force -Path $stage | Out-Null
Copy-Item -LiteralPath (Join-Path $project 'dist\LocalBroadcast.exe') -Destination $stage
foreach ($name in @('README.md', 'RELEASE_NOTES.md', 'BUILD_INFO.txt', 'install.ps1', 'configure-firewall.ps1')) {
    Copy-Item -LiteralPath (Join-Path $project $name) -Destination $stage
}
Copy-Item -LiteralPath (Join-Path $project 'docs') -Destination $stage -Recurse -Force
$archive = Join-Path $project ('dist\LocalBroadcast-' + $Version + '-windows-x64.zip')
Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $archive -Force
$hash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
Set-Content -LiteralPath ($archive + '.sha256') -Value ($hash + '  ' + (Split-Path $archive -Leaf)) -Encoding ascii
Write-Host $archive
