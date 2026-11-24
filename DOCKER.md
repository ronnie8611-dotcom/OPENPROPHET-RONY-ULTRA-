# OpenProphet en Docker (opcional)

El uso normal sigue siendo local con `START_AGENTS.bat`. Docker sirve para migrar a otro PC o servidor sin reinstalar Go/Python.

## Requisitos
- Docker Desktop (Windows) o Docker Engine (Linux).
- **IB Gateway/TWS sigue ejecutándose en el host** (es una app con GUI). El contenedor se conecta a `host.docker.internal:4002`.

## Arrancar

```powershell
docker compose up -d --build
```

El panel queda en `http://localhost:4534` (publicado solo en `127.0.0.1`).

## Parar / logs

```powershell
docker compose logs -f bot
docker compose down
```

## Qué se monta
- `data/` → configuración (`agent-config.json`, `strategies.json`), SQLite, baseline semanal, registry de modelos.
- `activity_logs/` → bitácora.
- `Backtest_Lab/` → scripts y datos del laboratorio.
- `model.json` → modelo XGBoost activo.
- `.env` → secretos; **no se copia dentro de la imagen**.

## Notas
- `IBKR_HOST` se sobrescribe a `host.docker.internal` en `docker-compose.yml`.
- `BIND_ADDR=0.0.0.0` solo dentro del contenedor; el puerto se publica únicamente en `127.0.0.1`.
- La imagen incluye Python + pandas/numpy/yfinance/xgboost/scikit-learn/scipy para que la pestaña Lab funcione. Es grande (~1 GB); si quieres una imagen pequeña, se puede separar el Lab en otro contenedor.
- En Linux, `host.docker.internal` se resuelve con `extra_hosts` (`host-gateway`).
