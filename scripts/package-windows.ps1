param([string]$Version = '0.3.8-beta.2')
$ErrorActionPreference = 'Stop'
$project = Split-Path $PSScriptRoot -Parent
$stage = Join-Path $project ('dist\package-' + $Version + '-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $stage | Out-Null
Copy-Item -LiteralPath (Join-Path $project 'dist\LocalBroadcast.exe') -Destination $stage
foreach ($name in @('README.md', 'RELEASE_NOTES.md', 'BUILD_INFO.txt', 'install.ps1', 'Install.cmd', 'Prepare-Offline.cmd', 'configure-firewall.ps1')) {
    Copy-Item -LiteralPath (Join-Path $project $name) -Destination $stage
}
Copy-Item -LiteralPath (Join-Path $project 'docs') -Destination $stage -Recurse -Force
Copy-Item -LiteralPath (Join-Path $project 'setup') -Destination $stage -Recurse -Force
Copy-Item -LiteralPath (Join-Path $project 'tools/obs-latency') -Destination (Join-Path $stage 'docs/obs-latency') -Recurse
# Windows PowerShell 5.1 needs a BOM to read Russian text correctly.
Get-ChildItem -LiteralPath $stage -Filter *.ps1 -Recurse | ForEach-Object {
    $text = [IO.File]::ReadAllText($_.FullName)
    [IO.File]::WriteAllText($_.FullName, $text, (New-Object Text.UTF8Encoding($true)))
}
$archive = Join-Path $project ('dist\LocalBroadcast-' + $Version + '-windows-x64.zip')
Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $archive -Force
$hash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
Set-Content -LiteralPath ($archive + '.sha256') -Value ($hash + '  ' + (Split-Path $archive -Leaf)) -Encoding ascii
Write-Host $archive
$payload = Join-Path $project 'installer\payload.zip'
Copy-Item -LiteralPath $archive -Destination $payload -Force
$setup = Join-Path $project ('dist\LocalBroadcast-' + $Version + '-Setup.exe')
Push-Location $project
try {
    go build -tags setup -trimpath -ldflags='-s -w' -o $setup ./installer
    if ($LASTEXITCODE -ne 0) { throw 'Setup build failed' }
} finally { Pop-Location }
$setupHash = (Get-FileHash -LiteralPath $setup -Algorithm SHA256).Hash.ToLowerInvariant()
Set-Content -LiteralPath ($setup + '.sha256') -Value ($setupHash + '  ' + (Split-Path $setup -Leaf)) -Encoding ascii
Write-Host $setup

# Core updates use the same production validator as the installed client.
$update = Join-Path $project ('dist\LocalBroadcast-' + $Version + '-Update-v1-win-x64.zip')
$builder = Start-Process -FilePath (Join-Path $stage 'LocalBroadcast.exe') -ArgumentList @('-package-version',$Version,'-make-update-package', ('"' + $update + '"'), '-update-notes', ('"' + (Join-Path $project 'RELEASE_NOTES.md') + '"')) -WindowStyle Hidden -Wait -PassThru
if ($builder.ExitCode -ne 0) { throw 'Update package build/validation failed; existing update archives must not be overwritten' }
Write-Host $update
