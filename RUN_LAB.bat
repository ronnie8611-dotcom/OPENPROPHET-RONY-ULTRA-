@echo off
setlocal
cd /d "%~dp0Backtest_Lab"
if not exist "%~dp0Backtest_Lab" (
  echo ERROR: no existe la carpeta Backtest_Lab junto a este .bat
  pause
  exit /b 1
)

set "PY=C:\Users\50688\AppData\Local\Python\pythoncore-3.15-64\python.exe"
if exist "%~dp0.env" (
  for /f "usebackq tokens=1,* delims==" %%A in ("%~dp0.env") do (
    if /i "%%A"=="PYTHON_PATH" set "PY=%%B"
  )
)

:menu
cls
echo ============================================
echo    OpenProphet Backtest Lab
echo    Carpeta: %CD%
echo ============================================
echo   1. Reentrenar modelo XGBoost y copiarlo a OpenProphet
echo   2. Validar Raschke (diagnostico del edge)
echo   3. Backtest completo FASE 1 (todas las estrategias/TF)
echo   4. Backtest Raschke con metricas correctas
echo   5. Buscar nuevos edges (CAMINO B)
echo   6. Abrir la carpeta del Lab
echo   0. Salir
echo.
set /p op=Opcion: 

if "%op%"=="1" (
  "%PY%" ml_trainer.py
  copy /Y "model.json" "%~dp0model.json"
  echo Modelo copiado a OpenProphet.
  pause
  goto menu
)
if "%op%"=="2" ( "%PY%" validate_raschke.py & pause & goto menu )
if "%op%"=="3" ( "%PY%" backtest_lab.py & pause & goto menu )
if "%op%"=="4" ( "%PY%" backtest_correcto.py & pause & goto menu )
if "%op%"=="5" ( "%PY%" camino_b_edges.py & pause & goto menu )
if "%op%"=="6" ( explorer "%~dp0Backtest_Lab" & goto menu )
if "%op%"=="0" exit /b 0
goto menu
