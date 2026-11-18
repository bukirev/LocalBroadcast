@echo off
set "LB_POWERSHELL=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"
if exist "%SystemRoot%\Sysnative\WindowsPowerShell\v1.0\powershell.exe" set "LB_POWERSHELL=%SystemRoot%\Sysnative\WindowsPowerShell\v1.0\powershell.exe"
"%LB_POWERSHELL%" -NoProfile -ExecutionPolicy Bypass -File "%~dp0setup\prepare-offline.ps1"
pause
