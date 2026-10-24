# autostart.ps1 - start Violink automatically when you log on to Windows.
#
# Creates a scheduled task "Violink" that:
#   - starts violink.exe (from this folder) 30 s after you log on
#   - keeps running while the PC is locked
#   - has no time limit (a normal scheduled task is stopped after 3 days!)
#   - is restarted every minute if it crashes, up to 999 times
#
# Run with install-autostart.bat / remove-autostart.bat, or:
#   powershell -ExecutionPolicy Bypass -File autostart.ps1 [-Remove]
#
# Note: a tray icon needs a logged-on user, so the task runs "only when the
# user is logged on". Locking the PC is fine, logging off stops Violink.

param([switch]$Remove)

$taskName = 'Violink'

if ($Remove) {
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    Write-Host "Scheduled task '$taskName' removed."
    exit 0
}

$exe = Join-Path $PSScriptRoot 'violink.exe'
if (-not (Test-Path $exe)) {
    Write-Error "violink.exe not found in $PSScriptRoot"
    exit 1
}

$user      = "$env:USERDOMAIN\$env:USERNAME"
$action    = New-ScheduledTaskAction -Execute $exe -WorkingDirectory $PSScriptRoot
$trigger   = New-ScheduledTaskTrigger -AtLogOn -User $user
$trigger.Delay = 'PT30S'
$settings  = New-ScheduledTaskSettingsSet `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
    -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -StartWhenAvailable -MultipleInstances IgnoreNew
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited

Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger `
    -Settings $settings -Principal $principal -Force `
    -Description 'Violink - scriptable I/O bridge' | Out-Null

Write-Host "Scheduled task '$taskName' created: $exe starts 30 s after $user logs on."
Write-Host "Start it now with:  Start-ScheduledTask -TaskName $taskName"
