# Установка в профиль текущего пользователя без прав администратора.
$ErrorActionPreference = 'Stop'
$source = Split-Path -Parent $MyInvocation.MyCommand.Path
$dest = Join-Path $env:LOCALAPPDATA 'Programs\LocalBroadcast'
New-Item -ItemType Directory -Force -Path $dest | Out-Null
Copy-Item (Join-Path $source 'LocalBroadcast.exe') (Join-Path $dest 'LocalBroadcast.exe') -Force
foreach ($name in @('ffmpeg.exe','ffplay.exe')) {
    $candidate = Join-Path $source $name
    if (Test-Path $candidate) { Copy-Item $candidate (Join-Path $dest $name) -Force }
}
$programs = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs'
$shortcutPath = Join-Path $programs 'Local Broadcast.lnk'
$shell = New-Object -ComObject WScript.Shell
$link = $shell.CreateShortcut($shortcutPath)
$link.TargetPath = Join-Path $dest 'LocalBroadcast.exe'
$link.WorkingDirectory = $dest
$link.Description = 'Local Broadcast — локальная видеосистема с OBS'
$link.Save()
Write-Host ('Установлено: ' + $dest)
Write-Host 'Запустите LocalBroadcast через меню Пуск. Для автозапуска выберите «Запускать с Windows» в настройках.'
Write-Host 'FFmpeg/FFplay не включены в архив: поместите их в папку приложения или задайте пути в настройках.'
