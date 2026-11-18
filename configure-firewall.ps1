# Запустить в PowerShell от администратора. Разрешает только Private/LocalSubnet.
$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Требуются права администратора. Запустите PowerShell от имени администратора.'
}
$entries = @(
    @{ name='LocalBroadcast Coordinator'; protocol='TCP'; ports='39780' },
    @{ name='LocalBroadcast Discovery'; protocol='UDP'; ports='39721' },
    @{ name='LocalBroadcast Presenter'; protocol='UDP'; ports='5000' },
    @{ name='LocalBroadcast Cameras'; protocol='UDP'; ports='5101-5999' },
    @{ name='LocalBroadcast SRT Presenter'; protocol='UDP'; ports='11000' },
    @{ name='LocalBroadcast SRT Cameras'; protocol='UDP'; ports='11101-11999' }
)
foreach ($e in $entries) {
    Get-NetFirewallRule -DisplayName $e.name -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    New-NetFirewallRule -DisplayName $e.name -Direction Inbound -Action Allow -Protocol $e.protocol `
        -LocalPort $e.ports -Profile Private -RemoteAddress LocalSubnet | Out-Null
    Write-Host ('OK: ' + $e.name)
}
Write-Host 'Правила добавлены только для частной сети / LocalSubnet.'
