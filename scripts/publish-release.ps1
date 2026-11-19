param([Parameter(Mandatory=$true)][string]$Tag, [Parameter(Mandatory=$true)][string]$ArtifactDirectory, [switch]$Publish)
$ErrorActionPreference = 'Stop'
$repo = 'bukirev/LocalBroadcast'
if ($Tag -notmatch '^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$') { throw 'Invalid release tag' }
$version = $Tag.Substring(1)
$files = @(Get-ChildItem -LiteralPath $ArtifactDirectory -File | Where-Object { $_.Name -in @(
    "LocalBroadcast-$version-windows-x64.zip", "LocalBroadcast-$version-windows-x64.zip.sha256",
    "LocalBroadcast-$version-Setup.exe", "LocalBroadcast-$version-Setup.exe.sha256",
    "LocalBroadcast-$version-Update-v1-win-x64.zip", "LocalBroadcast-$version-Update-v1-win-x64.zip.sha256",
    "LocalBroadcast-$version-Offline.zip", "LocalBroadcast-$version-Offline.zip.sha256") })
if ($files.Count -ne 8) { throw 'Expected installer, portable, core update, full offline ZIP and four SHA256 files' }
foreach ($file in @($files | Where-Object { $_.Extension -ne '.sha256' })) {
    $expected = (Get-Content -LiteralPath ($file.FullName + '.sha256') -Raw).Trim().Split(' ')[0]
    $actual = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -cne $expected) { throw ('Local SHA256 mismatch: ' + $file.Name) }
}
$notes = Join-Path (Split-Path $PSScriptRoot -Parent) 'RELEASE_NOTES.md'
$create = @('release','create',$Tag,'--repo',$repo,'--draft','--verify-tag','--title',("Local Broadcast $Tag"),'--notes-file',$notes)
if ($version.Contains('-')) { $create += @('--prerelease','--latest=false') }
$create += @($files | ForEach-Object FullName)
& gh @create
if ($LASTEXITCODE -ne 0) { throw 'Draft creation failed. Inspect existing release; never clobber public assets.' }
# Draft releases are not resolved reliably by REST /releases/tags/{tag}.
# Resolve the authenticated draft list once, then verify its numeric release ID.
$draftRaw = & gh api "repos/$repo/releases?per_page=100"
if ($LASTEXITCODE -ne 0) { throw 'Cannot list draft releases' }
$drafts = @(($draftRaw -join "`n" | ConvertFrom-Json) | Where-Object { $_.tag_name -ceq $Tag -and $_.draft })
if ($drafts.Count -ne 1) { throw 'Expected exactly one newly created draft' }
$releaseID = $drafts[0].id
$validated = $false
for ($attempt=0; $attempt -lt 6; $attempt++) {
    $raw = & gh api "repos/$repo/releases/$releaseID"
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read release metadata' }
    $release = ($raw -join "`n") | ConvertFrom-Json
    if (!$release.draft -or $release.tag_name -cne $Tag -or [bool]$release.prerelease -ne $version.Contains('-')) { throw 'Unexpected draft/channel' }
    $validated = $true
    foreach ($file in $files) {
        $assets = @($release.assets | Where-Object { $_.name -ceq $file.Name })
        $digest = 'sha256:' + (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($assets.Count -ne 1 -or $assets[0].state -cne 'uploaded' -or $assets[0].size -ne $file.Length -or $assets[0].digest -cne $digest) { $validated = $false }
    }
    if ($validated) { break }
    Start-Sleep -Seconds 3
}
if (!$validated) { throw 'Remote digest/size verification failed; draft remains unpublished' }
if ($Publish) {
    & gh release edit $Tag --repo $repo --draft=false
    if ($LASTEXITCODE -ne 0) { throw 'Publication not confirmed; inspect release state' }
} else { Write-Host 'Validated draft only; publication requires -Publish.' }
