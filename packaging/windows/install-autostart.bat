@echo off
rem Starts Violink automatically at logon (see autostart.ps1).
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0autostart.ps1"
pause
