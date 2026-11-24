# HANDOFF — OpenProphet

> **Punto de entrada para una tarea/chat nuevo de Cline.** Léelo junto con `projectbrief.md` antes de tocar código.
> **Última actualización:** 2026-11-17 14:00 (Tanda 16).

## 1. Estado actual

<!-- AUTO_STATUS_START -->
- **Fecha:** 2026-11-17 14:00
- **Go:** go1.27.4 windows/amd64
- **Check:** TODO OK (2026-11-17 13:58:11)
- **Bot:** 66 328 848 bytes, 2026-11-17 13:56
- **Disco libre (C:):** 16.36 GB
- **Ultimo backup:** tanda16_post_20261117_140500
<!-- AUTO_STATUS_END -->

- Ruta del proyecto: `C:\Users\50688\Desktop\AI_AGENTS\OpenProphet`
- Go: **1.27.4** (`go.mod`: `go 1.26.0`, `toolchain go1.27.4`).
- `CHECK.bat` / `check.ps1`: **TODO OK, exit 0**.
- `staticcheck 2026.2.2`: limpio.
- `govulncheck v1.9.0`: **0 vulnerabilidades alcanzables**.
- `bot.exe`: 66.250.672 bytes, compilado el 16/11/2026 21:04 con `go1.27.4`.
- Espacio libre en C:: **~2.6 GB**.
- Docker: **no instalado** (`docker` no se reconoce); bloqueado por falta de disco y WSL2 inactivo. Ver `DOCKER.md`.

### Backups

- `_Backups/tanda16_post_20261117_140500` — tabla Watchlist, búsqueda de compañías, catálogo y endpoints.
- `_Backups/tanda16_watchlist_table.png` — captura de la tabla con datos de ejemplo.
- `_Backups/tanda16_search_preview.png` — captura del autocompletado con “Dell”.
- `_Backups/tanda15_post_20261117_130200` — Observe/Operate, panel Watchlist, API y documentos.
- `_Backups/tanda15_pre_20261117_124955` — estado previo a Observe/Operate.
- `_Backups/tanda14_post_20261116_221600` — landing, Observatory refinado, `main.go` y documentos.
- `_Backups/tanda13_post_20261116_221000` — estado tras la UI Observatory (incluye `index.html`, documentos y reglas).
- `_Backups/tanda13_pre_20261116_220200` — estado previo a Observatory; incluye `observatory_preview.png`.
- `_Backups/tanda11_post_20261116_211319` — backup final (código, `go.mod`, `go.sum`, CI, Docker y `bot.exe`).
- `_Backups/tanda10_post_20261116_202007` — estado tras P0/memoria/opciones.
- `_Backups/bot.exe.before_p0_20261116.exe` — binario previo a los cambios P0.
- `_Backups/go.mod.before_vuln_fix` y `go.sum.before_vuln_fix` — antes de actualizar dependencias.
- `_Backups` y `_Archivo_Muerto` tienen prefijo `_` para que Go/staticcheck no escaneen código viejo.

## 2. Qué se hizo (Tandas 10–11)

- **Idempotencia/reconciliación**: `services/order_journal.go`, `JournalTradingService`, `ClientOrderID`/`SubmitState`, upsert por `order_id` o `client_order_id`, `ReconcilePendingOrders` al arrancar. Entradas fail-closed; salidas/protectores fail-open.
- **Opciones**: multiplicador 100× para símbolos con `:`; `services/options_guard.go` (`ValidateOptionsOrder`, `ValidateComboOrder`, `OptionExpiryDate`); validación en `agent_tools.go` y aprobación humana; 0DTE con fecha local; fail-closed si no hay precio.
- **Memoria**: `TradeMemory` con `Regime`, `RSI`, `ATRPercent`; `LogTradeResultWithContext`; `services/memory_similarity.go`; `get_memory` acepta contexto y devuelve `similar_trades`.
- **CI local**: `CHECK.bat`/`check.ps1` con gofmt (aviso CRLF), `go vet`, `go test -race`, `staticcheck`, `govulncheck` y `node --check`.
- **CI GitHub**: `.github/workflows/ci.yml` con Go 1.27.4 y `.github/dependabot.yml`.
- **Docker preparado**: `Dockerfile` (`golang:1.27-bookworm`), `docker-compose.yml` (solo `127.0.0.1:4534`, `IBKR_HOST=host.docker.internal`), `.dockerignore`, `DOCKER.md`.
- **Dependencias**: `x/net v0.60.0`, `x/text v0.42.0`, `grpc v1.83.1`; se pasó de 9 vulnerabilidades a 0 alcanzables.
- **Staticcheck**: 11 avisos corregidos (comparación siempre verdadera, errores con puntuación/mayúscula, `fmt.Sprintf` innecesario, `idx++` sin uso, funciones muertas, `TrimSuffix`).

## 3. Pendiente / decisiones

1. **AMZN**: ~85 acciones ≈ 66 % del NAV. Los candados 60 % desplegado / 20 % por instrumento bloquean órdenes nuevas. Decidir entre reducir a ~25 acciones o usar **Trades → Adopt Position** para que tenga stop real.
2. **Docker**: esperar a tener **≥10 GB libres**, WSL2/Hyper-V activo, permisos de admin y reinicio. Seguir `DOCKER.md`. No intentar instalar con ~2 GB.
3. **Tick-by-tick**: Fase 1 (barras de 1 s), Fase 2 (`reqHistoricalTicks` en `tws/`), Fase 3 (grabar `reqTickByTickData` y simular event-driven). **No implementado**.
4. **Opcional**: memoria con embeddings reales (Gemini/ONNX).
5. **Licencia**: OpenProphet es **CC BY-NC 4.0 (no comercial)**; revisar antes de publicar o vender.

## 4. Comandos útiles

```powershell
# Validación completa (manual; CHECK.bat tiene pause)
.\CHECK.bat

# Validación completa sin pause (mejor para automatización)
.\check.ps1

# Validación rápida mientras se programa
go build ./cmd/bot
go test ./services/ ./controllers/
staticcheck ./paquete_tocado

# Recompilar el binario
$env:GOTOOLCHAIN='go1.27.4'
$env:CGO_ENABLED=1
go build -o bot.exe ./cmd/bot
```

## 5. Reglas para el nuevo chat

- Leer `HANDOFF.md` y `projectbrief.md` antes de cambiar nada.
- No borrar `_Backups`, no ejecutar `go clean -cache` y no instalar/reiniciar Docker/WSL sin confirmación.
- No cambiar los límites 20/60/10 ni el time stop de 60 min sin confirmación.
- Usar siempre `GOTOOLCHAIN=go1.27.4`.
- Tras cambios de código: `go build` + tests; `.\check.ps1` solo al cerrar cambios de código o antes de paper. Para documentación, `.\update_docs.ps1`.
- No hacer commit ni push sin pedir permiso.
- Responder en español.

## 6. Prompt listo para pegar en un chat nuevo

```text
Lee @HANDOFF.md y @projectbrief.md antes de hacer nada.
Actúa como si hubieras estado en la sesión anterior de OpenProphet.
No cambies código todavía: primero resume el estado actual, lo que falta y el siguiente paso.
Respeta las reglas del HANDOFF. Pregunta antes de borrar backups, limpiar caché, instalar Docker o reiniciar WSL.
```

## 7. Registro de sesiones

> El agente debe añadir aquí una entrada nueva al cerrar cada tarea que cambie código, configuración, estrategias, CI, Docker o documentación. No borrar entradas anteriores.

### 2026-11-16 21:30 — Automatización de documentación
- **Hecho:** `HANDOFF.md`, `.clinerules`, `update_docs.ps1` y `UPDATE_DOCS.bat`; `check.ps1` ahora escribe `last_check.json` y refresca el estado de los documentos.
- **Validación:** `update_docs.ps1` ejecutado y `check.ps1` **TODO OK (exit 0)**; el bloque automático ya muestra `Check: TODO OK`.
- **Pendiente:** AMZN, Docker, tick-by-tick y embeddings.

### 2026-11-16 21:35 — Ajuste de cadencia de validación
- **Hecho:** `.clinerules` ahora indica ejecutar `check.ps1` solo al cerrar cambios de código o antes de operar en paper; para documentación se usa `update_docs.ps1`.
- **Validación:** cambio solo de reglas/documentación; no se ejecutó `CHECK.bat`.
- **Pendiente:** AMZN, Docker, tick-by-tick y embeddings.

### 2026-11-17 13:58 — Tabla Watchlist + búsqueda de compañías
- **Hecho:** Watchlist en una sola tabla con grupos **Allow/Block**, una columna por estrategia y modo `Inherit`/`Custom`; búsqueda por nombre o ticker; catálogo `data/symbols.json`; endpoints `GET /api/symbols/search` (local + Yahoo) y `GET /api/symbols/validate` (IBKR); metadatos `symbols` persistidos en `/api/watchlist`.
- **Validación:** `go build`, `go test -race`, `staticcheck`, `govulncheck`, `node --check` y `check.ps1` **TODO OK (exit 0)**; captura de tabla revisada.
- **Pendiente:** probar búsqueda Yahoo en vivo con red real, añadir rate limiting/caché persistente si hace falta y decidir si los nombres se muestran también en Agents. AMZN, Docker, tick-by-tick y embeddings siguen pendientes.

