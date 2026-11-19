param(
    [ValidateSet('Inspect','InspectAudio','RestartInput','ReopenInput','HardwareOn','Restore')][string]$Mode = 'Inspect',
    [ValidateRange(1024,65535)][int]$ApiPort = 39888,
    [string]$InputName = 'LB | Presenter',
    [string]$BackupPath = ''
)
$ErrorActionPreference = 'Stop'
$base = 'http://127.0.0.1:' + $ApiPort
$state = Invoke-RestMethod -Uri ($base + '/api/state') -TimeoutSec 5
if (!$BackupPath) { $BackupPath = Join-Path $env:LOCALAPPDATA 'LocalBroadcast\Diagnostics\obs-hw-decode-backup.json' }
$obsUri = [Uri]$state.config.obsUrl
if ($obsUri.Scheme -ne 'ws' -or $obsUri.Host -notin @('127.0.0.1','localhost','[::1]')) { throw 'OBS WebSocket must be local.' }
$socket = New-Object Net.WebSockets.ClientWebSocket
$script:sequence = 0
function Wait-OBSTask($Task) { $Task.GetAwaiter().GetResult() }
function Receive-OBS {
    $timeout = New-Object Threading.CancellationTokenSource
    $timeout.CancelAfter(5000)
    $buffer = New-Object byte[] 65536
    $memory = New-Object IO.MemoryStream
    try {
        do {
            $segment = New-Object 'ArraySegment[byte]' -ArgumentList @(,$buffer)
            $part = Wait-OBSTask ($socket.ReceiveAsync($segment,$timeout.Token))
            if ($part.MessageType -eq [Net.WebSockets.WebSocketMessageType]::Close) { throw 'OBS closed the connection.' }
            $memory.Write($buffer,0,$part.Count)
            if ($memory.Length -gt 1048576) { throw 'OBS response exceeds limit.' }
        } while (!$part.EndOfMessage)
        [Text.Encoding]::UTF8.GetString($memory.ToArray()) | ConvertFrom-Json
    } finally { $memory.Dispose(); $timeout.Dispose() }
}
function Send-OBS($Value) {
    $bytes = [Text.Encoding]::UTF8.GetBytes(($Value | ConvertTo-Json -Depth 16 -Compress))
    $segment = New-Object 'ArraySegment[byte]' -ArgumentList @(,$bytes)
    $timeout = New-Object Threading.CancellationTokenSource
    $timeout.CancelAfter(5000)
    try { [void](Wait-OBSTask ($socket.SendAsync($segment,[Net.WebSockets.WebSocketMessageType]::Text,$true,$timeout.Token))) }
    finally { $timeout.Dispose() }
}
function Get-OBSHash([string]$Text) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try { [Convert]::ToBase64String($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Text))) }
    finally { $sha.Dispose() }
}
function Invoke-OBS([string]$Kind, $Data = @{}) {
    $script:sequence++
    $id = 'lb-test-' + $script:sequence
    Send-OBS @{op=6;d=@{requestType=$Kind;requestId=$id;requestData=$Data}}
    while ($true) {
        $reply = Receive-OBS
        if ($reply.op -ne 7 -or $reply.d.requestId -ne $id) { continue }
        if (!$reply.d.requestStatus.result) { throw ('OBS request failed: ' + $Kind + ' (' + $reply.d.requestStatus.code + ')') }
        return $reply.d.responseData
    }
}
try {
    $timeout = New-Object Threading.CancellationTokenSource
    $timeout.CancelAfter(5000)
    try { [void](Wait-OBSTask ($socket.ConnectAsync($obsUri,$timeout.Token))) } finally { $timeout.Dispose() }
    $hello = Receive-OBS
    if ($hello.op -ne 0) { throw 'Unexpected OBS hello.' }
    $identify = @{rpcVersion=1;eventSubscriptions=$(if ($Mode -eq 'InspectAudio') {65536} else {0})}
    if ($hello.d.authentication) {
        $secret = Get-OBSHash ($state.config.obsPassword + $hello.d.authentication.salt)
        $identify.authentication = Get-OBSHash ($secret + $hello.d.authentication.challenge)
    }
    Send-OBS @{op=1;d=$identify}
    if ((Receive-OBS).op -ne 2) { throw 'OBS authentication failed.' }
    $version = Invoke-OBS 'GetVersion'
    $source = Invoke-OBS 'GetInputSettings' @{inputName=$InputName}
    if ($source.inputKind -ne 'ffmpeg_source') { throw 'Selected input is not a Media Source.' }
    $inputs = Invoke-OBS 'GetInputList'
    $identity = @($inputs.inputs | Where-Object { $_.inputName -ceq $InputName })
    if ($identity.Count -ne 1 -or !$identity[0].inputUuid) { throw 'Cannot uniquely identify OBS input.' }
    $defaults = Invoke-OBS 'GetInputDefaultSettings' @{inputKind='ffmpeg_source'}
    $currentHardware = [bool]$defaults.defaultInputSettings.hw_decode
    if ($source.inputSettings.PSObject.Properties['hw_decode']) { $currentHardware = [bool]$source.inputSettings.hw_decode }
    if ($Mode -in @('HardwareOn','Restore','RestartInput','ReopenInput')) {
        if ((Invoke-OBS 'GetStreamStatus').outputActive -or (Invoke-OBS 'GetRecordStatus').outputActive) { throw 'Stop OBS recording/streaming before changing the decoder.' }
        if ($Mode -eq 'ReopenInput') {
            if ($state.config.mode -ne 'master') { throw 'Run ReopenInput on the main PC.' }
            if ($source.inputSettings.is_local_file) { throw 'ReopenInput requires a network source, not a local file.' }
            $originalInput = [string]$source.inputSettings.input
            $inputUri = $null
            if (![Uri]::TryCreate($originalInput,[UriKind]::Absolute,[ref]$inputUri) -or $inputUri.Scheme -ne 'udp' -or $inputUri.Port -ne 5000) { throw 'ReopenInput requires the UDP:5000 presenter Media Source.' }
            # OBS 32 network Media Source update unconditionally recreates
            # media playback, even when the URL value is unchanged.
            [void](Invoke-OBS 'SetInputSettings' @{inputName=$InputName;inputSettings=@{input=$originalInput};overlay=$true})
            Start-Sleep -Milliseconds 500
            $restored = Invoke-OBS 'GetInputSettings' @{inputName=$InputName}
            if ($restored.inputSettings.input -cne $originalInput) { throw 'Input URL changed unexpectedly.' }
        } elseif ($Mode -eq 'RestartInput') {
            if ($state.config.mode -ne 'master') { throw 'Run RestartInput on the main PC.' }
            [void](Invoke-OBS 'TriggerMediaInputAction' @{inputName=$InputName;mediaAction='OBS_WEBSOCKET_MEDIA_INPUT_ACTION_RESTART'})
        } else {
        if ($Mode -eq 'HardwareOn') {
            if ($state.config.mode -ne 'master') { throw 'Run HardwareOn on the main PC.' }
            if (Test-Path -LiteralPath $BackupPath) { throw 'A backup already exists. Restore it before starting another test.' }
            $backup = @{settingKey='hw_decode';machine=[Environment]::MachineName;obsUrl=$obsUri.AbsoluteUri;inputName=$InputName;inputUuid=$identity[0].inputUuid;hardwareDecode=$currentHardware}
            $directory = Split-Path -Parent ([IO.Path]::GetFullPath($BackupPath))
            New-Item -ItemType Directory -Force -Path $directory | Out-Null
            $backup | ConvertTo-Json | Set-Content -LiteralPath $BackupPath -Encoding UTF8
            $target = $true
        } else {
            $backup = Get-Content -LiteralPath $BackupPath -Raw -Encoding UTF8 | ConvertFrom-Json
            if ($backup.settingKey -cne 'hw_decode') { throw 'Legacy hardware backup used an unsupported setting key; do not apply it to the real decoder.' }
            if ($backup.machine -cne [Environment]::MachineName -or $backup.obsUrl -cne $obsUri.AbsoluteUri -or $backup.inputName -cne $InputName -or $backup.inputUuid -cne $identity[0].inputUuid) { throw 'Backup belongs to a different machine or OBS input.' }
            $target = [bool]$backup.hardwareDecode
        }
        [void](Invoke-OBS 'SetInputSettings' @{inputName=$InputName;inputSettings=@{hw_decode=$target};overlay=$true})
        $check = Invoke-OBS 'GetInputSettings' @{inputName=$InputName}
        if ([bool]$check.inputSettings.hw_decode -ne $target) { throw 'Decoder setting was not confirmed.' }
        if ($Mode -eq 'Restore') { Remove-Item -LiteralPath $BackupPath }
        $currentHardware = $target
        }
    }
    $cpu = $null
    try { $cpu = @(Get-CimInstance Win32_Processor | Select-Object Name,NumberOfLogicalProcessors) } catch {}
    $stats = Invoke-OBS 'GetStats'
    $report = [ordered]@{mode=$Mode;appVersion=$state.version;obsVersion=$version.obsVersion;cpu=$cpu;srtHealth=$state.media.srtHealth;hardwareDecode=$currentHardware;bufferingMB=$source.inputSettings.buffering_mb;activeFPS=$stats.activeFps;renderSkippedFrames=$stats.renderSkippedFrames;renderTotalFrames=$stats.renderTotalFrames;backupPath=$(if ($Mode -eq 'HardwareOn') {$BackupPath} else {$null})}
    if ($Mode -eq 'InspectAudio') {
        function Read-OptionalOBS([string]$Kind,$Data=@{}) {
            try { Invoke-OBS $Kind $Data } catch { @{error=$_.Exception.Message} }
        }
        $audio = [ordered]@{}
        $audio['mute'] = Read-OptionalOBS 'GetInputMute' @{inputName=$InputName}
        $audio['volume'] = Read-OptionalOBS 'GetInputVolume' @{inputName=$InputName}
        $audio['monitor'] = Read-OptionalOBS 'GetInputAudioMonitorType' @{inputName=$InputName}
        $audio['tracks'] = Read-OptionalOBS 'GetInputAudioTracks' @{inputName=$InputName}
        $audio['syncOffset'] = Read-OptionalOBS 'GetInputAudioSyncOffset' @{inputName=$InputName}
        $audio['sourceActive'] = Read-OptionalOBS 'GetSourceActive' @{sourceName=$InputName}
        $audio['media'] = Read-OptionalOBS 'GetMediaInputStatus' @{inputName=$InputName}
        $audio['program'] = Read-OptionalOBS 'GetCurrentProgramScene'
        $studio = Read-OptionalOBS 'GetStudioModeEnabled'
        $audio['studioMode'] = $studio
        if ($studio.studioModeEnabled) { $audio['preview'] = Read-OptionalOBS 'GetCurrentPreviewScene' }
        $audio['stream'] = Read-OptionalOBS 'GetStreamStatus'
        $audio['record'] = Read-OptionalOBS 'GetRecordStatus'
        $bindings = @()
        foreach ($input in $inputs.inputs) {
            if ($input.inputKind -ne 'ffmpeg_source') { continue }
            $setting = Read-OptionalOBS 'GetInputSettings' @{inputName=$input.inputName}
            $uri = $null
            if ([Uri]::TryCreate([string]$setting.inputSettings.input,[UriKind]::Absolute,[ref]$uri) -and $uri.Scheme -eq 'udp' -and $uri.Port -eq 5000) { $bindings += $input.inputName }
        }
        $audio['inputsOnUDP5000'] = $bindings
        $eventCount = 0; $updates = 0; $peak = 0.0
        $end = [DateTime]::UtcNow.AddSeconds(3)
        while ([DateTime]::UtcNow -lt $end) {
            try { $event = Receive-OBS } catch { break }
            if ($event.op -ne 5 -or $event.d.eventType -ne 'InputVolumeMeters') { continue }
            $eventCount++
            foreach ($input in $event.d.eventData.inputs) {
                if ($input.inputName -cne $InputName -and $input.inputUuid -cne $identity[0].inputUuid) { continue }
                $updates++
                foreach ($channel in $input.inputLevelsMul) { foreach ($value in $channel) { if ([double]$value -gt $peak) { $peak = [double]$value } } }
            }
        }
        $audio['meter'] = @{eventCount=$eventCount;sourceUpdates=$updates;maxPeak=$peak;signalSeen=($peak -gt 0.000001)}
        $report['audio'] = $audio
    }
    $report | ConvertTo-Json -Depth 12
} finally { $socket.Dispose() }
