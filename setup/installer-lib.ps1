Set-StrictMode -Version Latest

function Set-JsonProperty($Object, [string]$Name, $Value) {
    $Object | Add-Member -MemberType NoteProperty -Name $Name -Value $Value -Force
}

function Write-SetupJson([string]$Path, $Value) {
    New-Item -ItemType Directory -Force -Path (Split-Path $Path -Parent) | Out-Null
    $temporary = $Path + '.setup-tmp'
    [IO.File]::WriteAllText($temporary, ($Value | ConvertTo-Json -Depth 30), (New-Object Text.UTF8Encoding($false)))
    Move-Item -LiteralPath $temporary -Destination $Path -Force
}

function Get-VerifiedDownload($Dependency, [string]$Path) {
    if (Test-Path -LiteralPath $Path) {
        if ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash -eq $Dependency.sha256) { return }
    }
    $uri = [uri]$Dependency.url
    if (!$uri.IsAbsoluteUri -or $uri.Scheme -ne 'https') { throw 'HTTPS download required' }
    New-Item -ItemType Directory -Force -Path (Split-Path $Path -Parent) | Out-Null
    Write-Host ('Downloading ' + $Dependency.version + ' ...')
    $partial = $Path + '.partial'
    $client = New-Object Net.WebClient
    try {
        $client.Headers['User-Agent'] = 'LocalBroadcast-Setup'
        $client.DownloadFile($Dependency.url, $partial)
    } finally { $client.Dispose() }
    if ((Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash -ne $Dependency.sha256) {
        throw ('SHA256 verification failed: ' + (Split-Path $Path -Leaf))
    }
    Move-Item -LiteralPath $partial -Destination $Path -Force
}

function Find-SetupOBS {
    $paths = @((Join-Path $env:ProgramFiles 'obs-studio\bin\64bit\obs64.exe'))
    $command = Get-Command obs64.exe -ErrorAction SilentlyContinue
    if ($command) { $paths += $command.Source }
    foreach ($path in $paths) {
        if ((Test-Path -LiteralPath $path) -and (Get-Item -LiteralPath $path).VersionInfo.FileMajorPart -ge 28) { return $path }
    }
    return ''
}

function Set-SetupConfiguration($Config, $Options, [string]$Destination, [string]$ObsPath = '', $Websocket = $null) {
    Set-JsonProperty $Config 'name' $Options.Name
    Set-JsonProperty $Config 'mode' $Options.Mode
    if ($Options.Secret) { Set-JsonProperty $Config 'secret' $Options.Secret }
    Set-JsonProperty $Config 'masterIp' $Options.MasterIP
    Set-JsonProperty $Config 'ffmpegPath' (Join-Path $Destination 'ffmpeg.exe')
    Set-JsonProperty $Config 'ffplayPath' (Join-Path $Destination 'ffplay.exe')
    Set-JsonProperty $Config 'launchAtLogin' $Options.AutoRun
    Set-JsonProperty $Config 'sendScreen' ($Options.Mode -eq 'presenter')
    Set-JsonProperty $Config 'sendCamera' ($Options.Mode -in @('camera', 'camera-view'))
    Set-JsonProperty $Config 'watchPresenter' ($Options.Mode -in @('viewer', 'camera-view'))
    Set-JsonProperty $Config 'autoStart' $true
    if ($Options.Mode -eq 'presenter') { Set-JsonProperty $Config 'screenAudioMode' 'wasapi' }
    if ($Options.Mode -eq 'master') {
        Set-JsonProperty $Config 'obsExePath' $ObsPath
        Set-JsonProperty $Config 'obsUrl' ('ws://127.0.0.1:' + $Websocket.server_port)
        Set-JsonProperty $Config 'obsPassword' $Websocket.server_password
        Set-JsonProperty $Config 'obsAutoSync' $true
        Set-JsonProperty $Config 'autoLaunchObs' $true
        Set-JsonProperty $Config 'transport' 'srt'
    }
    return $Config
}

function New-SetupWebsocket($Existing) {
    if ($null -eq $Existing) { $Existing = [pscustomobject]@{} }
    if (!$Existing.PSObject.Properties['server_port'] -or [int]$Existing.server_port -lt 1 -or [int]$Existing.server_port -gt 65535) {
        Set-JsonProperty $Existing 'server_port' 4455
    }
    if (!$Existing.PSObject.Properties['server_password'] -or [string]::IsNullOrWhiteSpace([string]$Existing.server_password)) {
        $bytes = New-Object byte[] 24
        $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
        try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
        Set-JsonProperty $Existing 'server_password' ([Convert]::ToBase64String($bytes))
    }
    Set-JsonProperty $Existing 'first_load' $false
    Set-JsonProperty $Existing 'server_enabled' $true
    Set-JsonProperty $Existing 'auth_required' $true
    return $Existing
}
