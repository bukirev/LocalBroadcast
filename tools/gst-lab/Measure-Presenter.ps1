param(
    [ValidateRange(5,14400)][int]$Seconds=7200,
    [ValidateSet('gstreamer','ffmpeg')][string]$Backend='gstreamer',
    [ValidateRange(1024,65535)][int]$ApiPort=39888,
    [string]$FFplay='ffplay.exe',
    [switch]$ExistingTone,
    [Parameter(Mandatory=$true)][string]$OutputCsv
)
$ErrorActionPreference='Stop'
$base="http://127.0.0.1:$ApiPort/api/state"
$state=Invoke-RestMethod $base -TimeoutSec 5
if (!$state.media.screen.running -or !$state.media.screen.targets) {throw 'Start a screen stream to a receiver first.'}
if ($state.media.camera.running) {throw 'Use a screen-only run for comparable process statistics.'}
$actual=$state.media.screen.mediaBackend
if (!$actual) {$actual='ffmpeg'}
if ($actual -ne $Backend) {throw "Expected $Backend. Active backend: $actual"}
if (Test-Path -LiteralPath $OutputCsv) {throw 'Choose a new output file.'}

$leaf=if($Backend -eq 'gstreamer'){'gst-launch-1.0.exe'}else{'ffmpeg.exe'}
$ownerId=(Get-NetTCPConnection -LocalPort $ApiPort -State Listen | Select-Object -First 1).OwningProcess
if (!$ExistingTone) {
$ffplayCommand=Get-Command $FFplay -CommandType Application
$tone=Start-Process $ffplayCommand.Source -ArgumentList @('-nodisp','-autoexit','-loglevel','error','-f','lavfi','-i',"sine=frequency=440:sample_rate=48000:duration=$($Seconds+5)",'-volume','8') -WindowStyle Hidden -PassThru
}
$timer=[Diagnostics.Stopwatch]::StartNew()
try {
    do {
        $state=Invoke-RestMethod $base -TimeoutSec 5
        $m=$state.media.screen
        $active=$m.mediaBackend
        if (!$active) {$active='ffmpeg'}
        if (($tone -and $tone.HasExited) -or !$m.running -or $active -ne $Backend) {throw 'Capture backend or control tone stopped.'}
        $children=@(Get-CimInstance Win32_Process -Filter "Name='$leaf' AND ParentProcessId=$ownerId")
        if($Backend -eq 'ffmpeg') {$children=@($children|Where-Object {$_.CommandLine -match 'gdigrab'})}
        $worker=if($children.Count -eq 1){Get-Process -Id $children[0].ProcessId -ErrorAction SilentlyContinue}
        [pscustomobject]@{
            time=[DateTimeOffset]::Now.ToString('o');elapsed=$timer.ElapsedMilliseconds;backend=$active;pid=$worker.Id
            cpuSeconds=$worker.CPU;rss=$worker.WorkingSet64;packets=$m.packetsForwarded;errors=$m.sendErrors
            targets=$m.targets;audioAgeMs=$m.audio.lastBufferAgeMs;peakDB=$m.audio.peakDB
            skippedFrames=$m.audio.skippedFrames;missingFrames=$m.audio.missingFrames
        }|Export-Csv -LiteralPath $OutputCsv -NoTypeInformation -Append
        Start-Sleep -Seconds 2
    } while($timer.Elapsed.TotalSeconds -lt $Seconds)
} finally {
    if($tone -and !$tone.HasExited){$tone.Kill();$tone.WaitForExit()}
    if($tone){$tone.Dispose()}
}
