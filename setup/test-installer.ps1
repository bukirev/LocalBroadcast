$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'installer-lib.ps1')
function Assert-Setup($Condition, [string]$Message) { if (!$Condition) { throw $Message } }
$project = Split-Path $PSScriptRoot -Parent
$fixture = Join-Path $project ('dist\test-installer-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixture -Force | Out-Null
try {
    Get-ChildItem -LiteralPath $project -Filter *.ps1 -Recurse | Where-Object { $_.FullName -notlike '*\dist\*' } | ForEach-Object {
        $tokens = $null; $errors = $null
        [Management.Automation.Language.Parser]::ParseFile($_.FullName, [ref]$tokens, [ref]$errors) | Out-Null
        Assert-Setup ($errors.Count -eq 0) ('PowerShell syntax error in ' + $_.FullName + ': ' + ($errors | Out-String))
    }
    $app = Join-Path $project 'dist\LocalBroadcast.exe'
    $path = Join-Path $fixture 'config.json'
    $process = Start-Process -FilePath $app -ArgumentList ('-init-config -config "' + $path + '"') -WindowStyle Hidden -Wait -PassThru
    Assert-Setup ($process.ExitCode -eq 0) 'Config initialization failed'
    $original = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
    $id = $original.nodeId; $secret = $original.secret
    $websocket = New-SetupWebsocket ([pscustomobject]@{server_port=4456; server_password='preserve-existing-password'; unrelated='keep'})
    Assert-Setup ($websocket.server_port -eq 4456 -and $websocket.server_password -eq 'preserve-existing-password' -and $websocket.unrelated -eq 'keep') 'OBS settings were lost'
    Assert-Setup ($websocket.server_enabled -and $websocket.auth_required -and !$websocket.first_load) 'OBS not enabled securely'
    $newWebsocket = New-SetupWebsocket $null
    Assert-Setup ($newWebsocket.server_password.Length -ge 24 -and $newWebsocket.server_port -eq 4455) 'OBS password was not generated'
    foreach ($mode in @('master', 'presenter', 'camera', 'camera-view', 'viewer')) {
        $options = [pscustomobject]@{Mode=$mode; Name='Installation test'; Secret=''; MasterIP=''; AutoRun=$true}
        $config = Set-SetupConfiguration $original $options $fixture 'C:\OBS\obs64.exe' $websocket
        Assert-Setup ($config.nodeId -eq $id -and $config.secret -eq $secret) 'Existing identity or key changed'
        Assert-Setup ($config.sendScreen -eq ($mode -eq 'presenter')) ('Screen flags incorrect for ' + $mode)
        Assert-Setup ($config.sendCamera -eq ($mode -in @('camera','camera-view'))) ('Camera flags incorrect for ' + $mode)
        Assert-Setup ($config.watchPresenter -eq ($mode -in @('viewer','camera-view'))) ('Preview flags incorrect for ' + $mode)
        Write-SetupJson $path $config
        $raw = [IO.File]::ReadAllBytes($path)
        Assert-Setup ($raw[0] -eq 123) 'Configuration must be UTF-8 without BOM'
        $process = Start-Process -FilePath $app -ArgumentList ('-init-config -config "' + $path + '"') -WindowStyle Hidden -Wait -PassThru
        Assert-Setup ($process.ExitCode -eq 0) ('Configuration validation failed for ' + $mode)
    }
    $file = Join-Path $fixture 'download.bin'; [IO.File]::WriteAllText($file, 'verified fixture')
    $digest = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash
    Get-VerifiedDownload ([pscustomobject]@{url='https://example.invalid/unused'; version='test'; sha256=$digest}) $file
    $rejected = $false
    try { Get-VerifiedDownload ([pscustomobject]@{url='http://example.invalid/unsafe'; version='test'; sha256='bad'}) $file } catch { $rejected = $true }
    Assert-Setup $rejected 'Insecure download accepted'
    Write-Host 'PASS: Windows PowerShell syntax, five roles, Go validation, identity preservation, OBS authentication, verified cache, HTTPS requirement.'
} finally {
    $resolved = [IO.Path]::GetFullPath($fixture)
    $expected = [IO.Path]::GetFullPath((Join-Path $project 'dist')) + [IO.Path]::DirectorySeparatorChar
    if (!$resolved.StartsWith($expected, [StringComparison]::OrdinalIgnoreCase)) { throw 'Refusing to remove fixture outside dist' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
