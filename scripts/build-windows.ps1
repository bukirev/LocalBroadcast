$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)
New-Item -ItemType Directory -Force dist | Out-Null
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go test ./...
if ($LASTEXITCODE -ne 0) { throw 'Go tests failed' }
go build -trimpath -ldflags='-s -w -H windowsgui' -o dist/LocalBroadcast.exe .
if ($LASTEXITCODE -ne 0) { throw 'Windows build failed' }
Write-Host 'Готово: dist/LocalBroadcast.exe'
Write-Host 'Скопируйте сюда FFmpeg.exe и FFplay.exe либо укажите их пути в программе.'
