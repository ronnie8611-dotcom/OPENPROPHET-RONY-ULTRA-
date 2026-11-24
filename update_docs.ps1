$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
$env:GOTOOLCHAIN = "go1.27.4"

function Write-TextFile([string]$Path, [string]$Text) {
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText((Join-Path $PSScriptRoot $Path), $Text, $utf8)
}

function Read-TextFile([string]$Path) {
    [System.IO.File]::ReadAllText((Join-Path $PSScriptRoot $Path))
}

$now = Get-Date
$stamp = $now.ToString("yyyy-MM-dd HH:mm")
$date = $now.ToString("yyyy-MM-dd")

$goVersion = "desconocido"
try {
    $rawGo = (& go version 2>$null)
    if ($LASTEXITCODE -eq 0 -and $rawGo) {
        $goVersion = ($rawGo -replace '^go version ', '').Trim()
    }
} catch { }

$freeGb = [math]::Round((Get-PSDrive C).Free / 1GB, 2)
$botText = "no encontrado"
if (Test-Path (Join-Path $PSScriptRoot "bot.exe")) {
    $bot = Get-Item (Join-Path $PSScriptRoot "bot.exe")
    $botText = ("{0:N0} bytes, {1}" -f $bot.Length, $bot.LastWriteTime.ToString("yyyy-MM-dd HH:mm"))
}

$checkText = "sin datos"
$checkPath = Join-Path $PSScriptRoot "last_check.json"
if (Test-Path $checkPath) {
    try {
        $lastCheck = Get-Content $checkPath -Raw | ConvertFrom-Json
        if ([int]$lastCheck.exit_code -eq 0) {
            $checkText = "TODO OK ($($lastCheck.timestamp))"
        } else {
            $checkText = "HAY FALLOS ($($lastCheck.timestamp))"
        }
    } catch { }
}

$latestBackup = Get-ChildItem (Join-Path $PSScriptRoot "_Backups") -Directory -ErrorAction SilentlyContinue |
    Sort-Object LastWriteTime -Descending | Select-Object -First 1
$backupText = if ($latestBackup) { $latestBackup.Name } else { "sin backups" }

$handoffBlock = @"
<!-- AUTO_STATUS_START -->
- **Fecha:** $stamp
- **Go:** $goVersion
- **Check:** $checkText
- **Bot:** $botText
- **Disco libre (C:):** $freeGb GB
- **Ultimo backup:** $backupText
<!-- AUTO_STATUS_END -->
"@

$briefBlock = @"
<!-- AUTO_STATUS_START -->
**Estado automatico:** $stamp | Go $goVersion | Check: $checkText | Bot: $botText | Disco C: $freeGb GB
<!-- AUTO_STATUS_END -->
"@

function Update-Block([string]$path, [string]$block, [string]$anchor) {
    $full = Join-Path $PSScriptRoot $path
    if (-not (Test-Path $full)) { return }
    $text = [System.IO.File]::ReadAllText($full)
    $pattern = '(?s)<!-- AUTO_STATUS_START -->.*?<!-- AUTO_STATUS_END -->'
    if ($text -match $pattern) {
        $evaluator = [System.Text.RegularExpressions.MatchEvaluator]{ param($m) $block }
        $text = [regex]::Replace($text, $pattern, $evaluator)
    } else {
        $insert = [System.Text.RegularExpressions.MatchEvaluator]{ param($m) $m.Value + "`r`n`r`n" + $block }
        $text = [regex]::Replace($text, $anchor, $insert)
    }
    Write-TextFile $path $text
}

Update-Block "HANDOFF.md" $handoffBlock '(?m)^## 1\. Estado actual\s*$'
Update-Block "projectbrief.md" $briefBlock '(?m)^\*\*M.dulo Go:\*\*.*$'

$handoff = Read-TextFile "HANDOFF.md"
$handoffDatePattern = '(?m)^(> \*\*.*?ltima actualizaci.*?\*\* )\d{4}-\d{2}-\d{2} \d{2}:\d{2}'
$handoffDateEval = [System.Text.RegularExpressions.MatchEvaluator]{ param($m) $m.Groups[1].Value + $stamp }
$handoff = [regex]::Replace($handoff, $handoffDatePattern, $handoffDateEval)
Write-TextFile "HANDOFF.md" $handoff

$brief = Read-TextFile "projectbrief.md"
$briefDatePattern = '(?m)^(\*\*.*?ltima actualizaci.*?\*\* )\d{4}-\d{2}-\d{2}'
$briefDateEval = [System.Text.RegularExpressions.MatchEvaluator]{ param($m) $m.Groups[1].Value + $date }
$brief = [regex]::Replace($brief, $briefDatePattern, $briefDateEval)
Write-TextFile "projectbrief.md" $brief

Write-Host "Docs actualizados: HANDOFF.md y projectbrief.md ($stamp)" -ForegroundColor Green
