$ErrorActionPreference = "Continue"
Set-Location $PSScriptRoot
$env:GOTOOLCHAIN = "go1.27.4"
$fail = $false

Write-Host "`n=== gofmt ===" -ForegroundColor Cyan
$bad = @(gofmt -l . | Where-Object { $_ -notmatch '_Archivo_Muerto' -and $_ -notmatch '_Backups' })
if ($bad.Count -gt 0) {
  $bad | ForEach-Object { Write-Host "  AVISO sin formatear: $_" -ForegroundColor Yellow }
  Write-Host "AVISO: en Windows los archivos CRLF aparecen como no formateados. El CI de Linux es el que lo valida." -ForegroundColor Yellow
} else {
  Write-Host "OK"
}

$packages = @(
  "./backtest",
  "./cmd/...",
  "./config/...",
  "./configstore/...",
  "./controllers/...",
  "./database/...",
  "./interfaces/...",
  "./models/...",
  "./services/...",
  "./tws/..."
)

Write-Host "`n=== go vet ===" -ForegroundColor Cyan
go vet $packages
if ($LASTEXITCODE -ne 0) { $fail = $true }

Write-Host "`n=== go test -race ===" -ForegroundColor Cyan
go test -race $packages
if ($LASTEXITCODE -ne 0) { $fail = $true }

Write-Host "`n=== staticcheck ===" -ForegroundColor Cyan
if (Get-Command staticcheck -ErrorAction SilentlyContinue) {
  staticcheck $packages
  if ($LASTEXITCODE -ne 0) { $fail = $true }
} else {
  Write-Host "SKIP: staticcheck no instalado. Instala con: go install honnef.co/go/tools/cmd/staticcheck@latest"
}

Write-Host "`n=== govulncheck ===" -ForegroundColor Cyan
if (Get-Command govulncheck -ErrorAction SilentlyContinue) {
  govulncheck $packages
  if ($LASTEXITCODE -ne 0) { $fail = $true }
} else {
  Write-Host "SKIP: govulncheck no instalado. Instala con: go install golang.org/x/vuln/cmd/govulncheck@latest"
}

Write-Host "`n=== UI syntax (node --check) ===" -ForegroundColor Cyan
if (Get-Command node -ErrorAction SilentlyContinue) {
  $html = Get-Content "agent/public/index.html" -Raw
  $matches = [regex]::Matches($html, '(?s)<script>(.*?)</script>')
  if ($matches.Count -gt 0) {
    $tmp = Join-Path $env:TEMP "openprophet_ui_check.js"
    [System.IO.File]::WriteAllText($tmp, $matches[0].Groups[1].Value)
    node --check $tmp
    if ($LASTEXITCODE -ne 0) { $fail = $true }
  }
} else {
  Write-Host "SKIP: node no instalado"
}

if ($fail) {
  Write-Host "`nHAY FALLOS. Revisa la salida anterior." -ForegroundColor Red
} else {
  Write-Host "`nTODO OK" -ForegroundColor Green
}

$exitCode = if ($fail) { 1 } else { 0 }
$status = if ($fail) { "FAIL" } else { "OK" }
try {
  $result = [ordered]@{
    timestamp = (Get-Date).ToString("yyyy-MM-dd HH:mm:ss")
    exit_code = $exitCode
    status = $status
  }
  $result | ConvertTo-Json | Set-Content "last_check.json" -Encoding UTF8
  & "$PSScriptRoot\update_docs.ps1"
} catch {
  Write-Host "AVISO: no se pudieron actualizar HANDOFF.md/projectbrief.md: $_" -ForegroundColor Yellow
}

exit $exitCode
