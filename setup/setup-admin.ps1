param(
    [Parameter(Mandatory=$true)][string]$CacheDirectory,
    [Parameter(Mandatory=$true)][string]$ResultPath,
    [switch]$InstallOBS,
    [switch]$PrivateNetwork
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'installer-lib.ps1')
$result = [pscustomobject]@{ success = $false; reboot = $false; error = '' }
try {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (!$principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Administrator permission is required.' }
    $dependencies = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'dependencies.json') -Raw | ConvertFrom-Json
    if ($InstallOBS) {
        foreach ($item in @(@{key='vcredist'; file='VC_redist.x64.exe'; arguments='/install /quiet /norestart'; codes=@(0,1638,3010)}, @{key='obs'; file='OBS-Installer.exe'; arguments='/S'; codes=@(0,3010)})) {
            $path = Join-Path $CacheDirectory $item.file
            if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $dependencies.($item.key).sha256) { throw ('Invalid installer checksum: ' + $item.file) }
            $process = Start-Process -FilePath $path -ArgumentList $item.arguments -WindowStyle Hidden -Wait -PassThru
            if ($process.ExitCode -notin $item.codes) { throw ($item.file + ' failed, exit code ' + $process.ExitCode) }
            if ($process.ExitCode -eq 3010) { $result.reboot = $true }
        }
    }
    if ($PrivateNetwork) {
        $adapters = @(Get-NetAdapter -Physical | Where-Object { $_.Status -eq 'Up' })
        foreach ($adapter in $adapters) {
            Get-NetConnectionProfile -InterfaceIndex $adapter.InterfaceIndex -ErrorAction SilentlyContinue |
                Where-Object { $_.NetworkCategory -eq 'Public' } |
                Set-NetConnectionProfile -NetworkCategory Private
        }
    }
    & (Join-Path (Split-Path $PSScriptRoot -Parent) 'configure-firewall.ps1')
    $result.success = $true
} catch { $result.error = $_.Exception.Message }
Write-SetupJson $ResultPath $result
if (!$result.success) { exit 1 }
