@echo off
setlocal
cd /d "%~dp0Backtest_Lab"
if not exist "%~dp0Backtest_Lab" (
  echo ERROR: no existe la carpeta Backtest_Lab junto a este .bat
  exit /b 1
)

rem Lee PYTHON_PATH desde .env (si existe)
set "PY=C:\Users\50688\AppData\Local\Python\pythoncore-3.15-64\python.exe"
if exist "%~dp0.env" (
  for /f "usebackq tokens=1,* delims==" %%A in ("%~dp0.env") do (
    if /i "%%A"=="PYTHON_PATH" set "PY=%%B"
  )
)

echo Iniciando Auto-Reentrenamiento del Modelo XGBoost... >> retrain_log.txt
date /t >> retrain_log.txt
time /t >> retrain_log.txt

"%PY%" ml_trainer.py >> retrain_log.txt 2>&1

echo Copiando nuevo modelo a OpenProphet... >> retrain_log.txt
copy /Y "model.json" "%~dp0model.json" >> retrain_log.txt 2>&1

echo Reentrenamiento completado. >> retrain_log.txt
echo ---------------------------------------- >> retrain_log.txt