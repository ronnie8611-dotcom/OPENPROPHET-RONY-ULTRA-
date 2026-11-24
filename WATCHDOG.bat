@echo off
setlocal
cd /d "%~dp0"
echo [%date% %time%] Watchdog iniciado. >> watchdog.log
:loop
tasklist /FI "IMAGENAME eq bot.exe" | find /I "bot.exe" >nul
if errorlevel 1 (
  echo [%date% %time%] bot.exe no esta corriendo. Reiniciando... >> watchdog.log
  start "" "%~dp0bot.exe"
  timeout /t 20 /nobreak >nul
)
timeout /t 60 /nobreak >nul
goto loop
