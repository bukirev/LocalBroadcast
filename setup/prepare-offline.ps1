param([string]$SourceDirectory = '', [string]$Destination = '', [string]$CacheDirectory = '')
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
. (Join-Path $PSScriptRoot 'installer-lib.ps1')
if (!$SourceDirectory) { $SourceDirectory = Split-Path $PSScriptRoot -Parent }
if (!$Destination) { $Destination = $SourceDirectory }
if (!$CacheDirectory) { $CacheDirectory = Join-Path $env:LOCALAPPDATA 'LocalBroadcast\Downloads' }
$source = [IO.Path]::GetFullPath($SourceDirectory)
if (!(Test-Path -LiteralPath (Join-Path $source 'LocalBroadcast.exe'))) { throw 'Сначала распакуйте ZIP программы. Запускайте Prepare-Offline.cmd из распакованной папки.' }
$info = Get-Content -LiteralPath (Join-Path $source 'BUILD_INFO.txt') -First 1
if ($info -notmatch '(?<![0-9A-Za-z])((?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?)(?![0-9A-Za-z])') { throw 'Не найдена версия пакета.' }
$version = $Matches[1]
$package = [IO.Path]::GetFullPath((Join-Path $Destination ('LocalBroadcast-' + $version + '-Offline')))
if ($package -eq $source -or $package.StartsWith((Join-Path $source 'setup') + '\', [StringComparison]::OrdinalIgnoreCase) -or $package.StartsWith((Join-Path $source 'docs') + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Выберите другую папку назначения.' }
New-Item -ItemType Directory -Force -Path $package | Out-Null
foreach ($name in @('LocalBroadcast.exe','README.md','RELEASE_NOTES.md','BUILD_INFO.txt','Install.cmd','Prepare-Offline.cmd','install.ps1','configure-firewall.ps1')) {
    Copy-Item -LiteralPath (Join-Path $source $name) -Destination $package -Force
}
foreach ($name in @('setup','docs')) { Copy-Item -LiteralPath (Join-Path $source $name) -Destination $package -Recurse -Force }
$dependencies = Get-Content -LiteralPath (Join-Path $source 'setup\dependencies.json') -Raw | ConvertFrom-Json
$files = [ordered]@{gstreamer='gstreamer-1.26.10.msi';ffmpeg='ffmpeg-9.0.2.zip'; obs='OBS-Installer.exe'; vcredist='VC_redist.x64.exe'}
$directory = Join-Path $package 'dependencies'
New-Item -ItemType Directory -Force -Path $directory | Out-Null
Write-Host 'Подготовка полного пакета. Компоненты скачиваются с сайтов их разработчиков и проверяются по SHA256.'
foreach ($key in $files.Keys) { Get-VerifiedDownload $dependencies.$key (Join-Path $directory $files[$key]) $CacheDirectory }
$sources = @('Dependencies are original upstream distributions, kept intact with their license files.', 'FFmpeg GPLv3: https://www.gyan.dev/ffmpeg/builds/ ; https://github.com/GyanD/codexffmpeg/releases/tag/9.0.2', 'FFmpeg source: https://github.com/FFmpeg/FFmpeg/commit/946fcce07b', 'GStreamer 1.26.10: https://gstreamer.freedesktop.org/src/ ; runtime plugins retain their component licenses.', 'OBS source and license: https://github.com/obsproject/obs-studio', 'Microsoft Runtime: https://visualstudio.microsoft.com/downloads/')
Set-Content -LiteralPath (Join-Path $package 'DEPENDENCY-SOURCES.txt') -Value $sources -Encoding utf8
$archive = Join-Path $Destination ('LocalBroadcast-' + $version + '-Offline.zip')
Write-Host 'Упаковка автономного архива...'
Compress-Archive -Path (Join-Path $package '*') -DestinationPath $archive -CompressionLevel NoCompression -Force
$digest = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
Set-Content -LiteralPath ($archive + '.sha256') -Value ($digest + '  ' + (Split-Path $archive -Leaf)) -Encoding ascii
Write-Host ('Готово: ' + $archive)
Write-Host 'Перенесите ZIP на другой ПК, распакуйте и запустите Install.cmd. Интернет для установки компонентов не нужен.'
