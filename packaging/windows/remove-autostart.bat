@echo off
rem Removes the Violink autostart task.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0autostart.ps1" -Remove
pause
