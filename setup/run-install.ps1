$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$ProgressPreference = 'SilentlyContinue'
$source = Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'installer-lib.ps1')
. (Join-Path $PSScriptRoot 'wizard.ps1')
$transcript = $false
$ffmpegStage = $null
try {
    if (![Environment]::Is64BitOperatingSystem -or ![Environment]::Is64BitProcess) { throw 'Требуется Windows 10/11 x64 и 64-разрядный PowerShell.' }
    $destination = Join-Path $env:LOCALAPPDATA 'Programs\LocalBroadcast'
    $configPath = Join-Path $env:APPDATA 'LocalBroadcast\config.json'
    $cache = Join-Path $env:LOCALAPPDATA 'LocalBroadcast\Downloads'
    New-Item -ItemType Directory -Force -Path $cache | Out-Null
    Start-Transcript -Path (Join-Path $cache 'install.log') -Force | Out-Null; $transcript = $true
    $existing = $null
    if (Test-Path -LiteralPath $configPath) { $existing = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json }
    $options = Show-SetupOptions $existing
    if ($null -eq $options) { return }
    $session = (Get-Process -Id $PID).SessionId
    $running = @(Get-Process -Name LocalBroadcast -ErrorAction SilentlyContinue | Where-Object { $_.SessionId -eq $session })
    if ($running.Count) {
        # New versions exit cleanly; older versions are closed only after the user starts installation.
        try { Invoke-RestMethod 'http://127.0.0.1:39888/api/action' -Method Post -ContentType 'application/json' -Headers @{'X-LB-UI'='1'} -Body '{"action":"shutdown"}' -TimeoutSec 2 | Out-Null } catch { }
        foreach ($process in $running) {
            if (!$process.WaitForExit(2000)) { Stop-Process -Id $process.Id -ErrorAction Stop }
        }
    }
    if ($options.Mode -eq 'master') { Wait-SetupClosed 'obs64' 'OBS Studio' }
    $dependencies = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'dependencies.json') -Raw | ConvertFrom-Json
    Write-Host '1/5. Подготовка FFmpeg и FFplay...'
    $ffmpegArchive = Join-Path $cache 'ffmpeg-9.0.2.zip'
    Get-VerifiedDownload $dependencies.ffmpeg $ffmpegArchive
    $ffmpegStage = Join-Path $cache ('ffmpeg-' + [guid]::NewGuid().ToString('N'))
    Expand-Archive -LiteralPath $ffmpegArchive -DestinationPath $ffmpegStage
    $ffmpeg = @(Get-ChildItem -LiteralPath $ffmpegStage -Recurse -Filter ffmpeg.exe)
    $ffplay = @(Get-ChildItem -LiteralPath $ffmpegStage -Recurse -Filter ffplay.exe)
    if ($ffmpeg.Count -ne 1 -or $ffplay.Count -ne 1) { throw 'В архиве не найдены FFmpeg и FFplay.' }
    $protocols = & $ffmpeg[0].FullName -hide_banner -protocols 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0 -or $protocols -notmatch '(?m)^\s*srt\s*$') { throw 'FFmpeg не поддерживает SRT.' }
    if ($options.Mode -in @('camera', 'camera-view')) {
        $oldPreference = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
        $deviceOutput = & $ffmpeg[0].FullName -hide_banner -list_devices true -f dshow -i dummy 2>&1 | Out-String
        $ErrorActionPreference = $oldPreference
        $devices = @([regex]::Matches($deviceOutput, '"([^"]+)"\s+\(video\)') | ForEach-Object { $_.Groups[1].Value } | Select-Object -Unique)
        $camera = Select-SetupCamera $devices
    }
    Write-Host '2/5. Подготовка OBS и брандмауэра...'
    $obsPath = Find-SetupOBS
    $needOBS = $options.Mode -eq 'master' -and !$obsPath
    if ($needOBS) {
        Get-VerifiedDownload $dependencies.vcredist (Join-Path $cache 'VC_redist.x64.exe')
        Get-VerifiedDownload $dependencies.obs (Join-Path $cache 'OBS-Installer.exe')
    }
    $resultPath = Join-Path $cache ('admin-result-' + [guid]::NewGuid().ToString('N') + '.json')
    $adminScript = Join-Path $PSScriptRoot 'setup-admin.ps1'
    $arguments = '-NoProfile -ExecutionPolicy Bypass -File "' + $adminScript + '" -CacheDirectory "' + $cache + '" -ResultPath "' + $resultPath + '"'
    if ($needOBS) { $arguments += ' -InstallOBS' }
    if ($options.PrivateNetwork) { $arguments += ' -PrivateNetwork' }
    $admin = Start-Process -FilePath (Join-Path $PSHOME 'powershell.exe') -ArgumentList $arguments -Verb RunAs -WindowStyle Hidden -Wait -PassThru
    if (!(Test-Path -LiteralPath $resultPath)) { throw 'Системная настройка не завершена. Разрешите запрос Windows и повторите установку.' }
    $adminResult = Get-Content -LiteralPath $resultPath -Raw | ConvertFrom-Json
    if ($admin.ExitCode -ne 0 -or !$adminResult.success) { throw ('Системная настройка: ' + $adminResult.error) }
    if ($options.Mode -eq 'master') {
        $obsPath = Find-SetupOBS
        if (!$obsPath) { throw 'Установленный OBS не найден. Перезагрузите Windows при необходимости и повторите установку.' }
    }
    Write-Host '3/5. Установка программы...'
    New-Item -ItemType Directory -Force -Path $destination | Out-Null
    foreach ($file in @('LocalBroadcast.exe', 'README.md', 'RELEASE_NOTES.md', 'BUILD_INFO.txt')) {
        Copy-Item -LiteralPath (Join-Path $source $file) -Destination $destination -Force
    }
    Copy-Item -LiteralPath $ffmpeg[0].FullName -Destination (Join-Path $destination 'ffmpeg.exe') -Force
    Copy-Item -LiteralPath $ffplay[0].FullName -Destination (Join-Path $destination 'ffplay.exe') -Force
    $licenseDirectory = Join-Path $destination 'third-party\ffmpeg'
    New-Item -ItemType Directory -Force -Path $licenseDirectory | Out-Null
    $ffmpegRoot = Split-Path $ffmpeg[0].DirectoryName -Parent
    Get-ChildItem -LiteralPath $ffmpegRoot -File | ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination $licenseDirectory -Force }
    Write-Host '4/5. Настройка роли и OBS...'
    $backupSuffix = '.before-setup-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [guid]::NewGuid().ToString('N')
    if (Test-Path -LiteralPath $configPath) { Copy-Item -LiteralPath $configPath -Destination ($configPath + $backupSuffix) }
    $app = Join-Path $destination 'LocalBroadcast.exe'
    $init = Start-Process -FilePath $app -ArgumentList ('-init-config -config "' + $configPath + '"') -WindowStyle Hidden -Wait -PassThru
    if ($init.ExitCode -ne 0) { throw 'Ошибка чтения существующей конфигурации. Резервная копия сохранена.' }
    $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    $websocket = $null
    if ($options.Mode -eq 'master') {
        $obsDirectory = Join-Path $env:APPDATA 'obs-studio'
        foreach ($relative in @('global.ini', 'plugin_config\obs-websocket\config.json')) {
            $settingsPath = Join-Path $obsDirectory $relative
            if (Test-Path -LiteralPath $settingsPath) { Copy-Item -LiteralPath $settingsPath -Destination ($settingsPath + $backupSuffix) }
        }
        $websocket = New-SetupWebsocket (Read-SetupObsWebsocket $obsDirectory)
        Write-SetupObsWebsocket $obsDirectory $websocket
    }
    $config = Set-SetupConfiguration $config $options $destination $obsPath $websocket
    if ($options.Mode -in @('camera', 'camera-view')) { Set-JsonProperty $config 'cameraDevice' $camera }
    Write-SetupJson $configPath $config
    $validate = Start-Process -FilePath $app -ArgumentList ('-init-config -config "' + $configPath + '"') -WindowStyle Hidden -Wait -PassThru
    if ($validate.ExitCode -ne 0) { throw 'Не удалось проверить настройки. Проверьте install.log.' }
    Write-Host '5/5. Ярлыки и запуск...'
    $shell = New-Object -ComObject WScript.Shell
    foreach ($folder in @([Environment]::GetFolderPath('Programs'), [Environment]::GetFolderPath('Desktop'))) {
        $link = $shell.CreateShortcut((Join-Path $folder 'Local Broadcast.lnk'))
        $link.TargetPath = $app; $link.WorkingDirectory = $destination; $link.Save()
    }
    $runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
    if ($options.AutoRun) {
        if (!(Test-Path -LiteralPath $runKey)) { New-Item -Path $runKey | Out-Null }
        Set-ItemProperty -Path $runKey -Name LocalBroadcast -Value ('"' + $app + '" -no-browser')
    } else { Remove-ItemProperty -Path $runKey -Name LocalBroadcast -ErrorAction SilentlyContinue }
    Start-Process -FilePath $app -WorkingDirectory $destination -WindowStyle Hidden
    $connected = $false
    $deadline = (Get-Date).AddSeconds(12)
    while ((Get-Date) -lt $deadline) {
        try {
            $state = Invoke-RestMethod 'http://127.0.0.1:39888/api/state' -TimeoutSec 1
            if ($state.config.nodeId -eq $config.nodeId -and $state.bootMode -eq $options.Mode) { $connected = $true; break }
        } catch { }
        Start-Sleep -Milliseconds 250
    }
    if (!$connected) { throw 'Файлы установлены, но панель управления не запустилась. Проверьте, свободен ли порт 39888, и повторите запуск Local Broadcast.' }
    $message = 'Установка завершена. Открыто окно управления Windows. Настройки доступны по кнопке «Панель в браузере».'
    if ($options.Mode -eq 'master') { $message += "`nОбщий ключ для остальных ПК находится в разделе «Настройки» панели." }
    if ($adminResult.reboot) { $message += "`nMicrosoft Runtime запросил перезагрузку Windows. Сохраните работу и перезагрузите ПК." }
    [Windows.Forms.MessageBox]::Show($message, 'Local Broadcast', 'OK', 'Information') | Out-Null
} catch {
    Write-Host $_.Exception.Message -ForegroundColor Red
    [Windows.Forms.MessageBox]::Show(($_.Exception.Message + "`nУстановка не завершена. Повторный запуск безопасен.`nЖурнал: " + (Join-Path $env:LOCALAPPDATA 'LocalBroadcast\Downloads\install.log')), 'Ошибка установки', 'OK', 'Error') | Out-Null
    exit 1
} finally {
    if ($ffmpegStage -and (Test-Path -LiteralPath $ffmpegStage)) {
        $resolved = [IO.Path]::GetFullPath($ffmpegStage)
        $expected = [IO.Path]::GetFullPath($cache) + [IO.Path]::DirectorySeparatorChar
        if ($resolved.StartsWith($expected, [StringComparison]::OrdinalIgnoreCase) -and (Split-Path $resolved -Leaf).StartsWith('ffmpeg-')) {
            Remove-Item -LiteralPath $resolved -Recurse -Force
        }
    }
    if ($transcript) { Stop-Transcript | Out-Null }
}
