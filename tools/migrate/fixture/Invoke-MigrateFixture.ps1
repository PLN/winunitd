param(
    [Parameter(Mandatory = $true)][string]$MigrateExe
)

$ErrorActionPreference = 'Stop'

function Invoke-MigrateStep {
    param(
        [string]$Title,
        [string[]]$MigrateArgs,
        [int]$Expect
    )
    & $MigrateExe @MigrateArgs
    $code = $LASTEXITCODE
    if ($code -ne $Expect) {
        throw "$Title exited $code, expected $Expect"
    }
}

$base = Join-Path $env:TEMP 'winunitd-r65-fixture'
if (Test-Path $base) {
    Remove-Item -Recurse -Force $base
}
New-Item -ItemType Directory -Path $base | Out-Null
$msi = Join-Path $base 'caller-supplied.msi'
[System.IO.File]::WriteAllBytes($msi, [byte[]](1, 2, 3, 4))

$happy = Join-Path $base 'happy'
$happyBackup = Join-Path $base 'backup-happy'
Invoke-MigrateStep 'init' @('-root', $happy, 'init', '-scenario', 'happy') 0
Invoke-MigrateStep 'discover' @('-root', $happy, 'discover') 0
Invoke-MigrateStep 'backup' @('-root', $happy, 'backup', '-out', $happyBackup) 0
Invoke-MigrateStep 'dry-run' @('-root', $happy, 'dry-run', '-msi', $msi) 0
Invoke-MigrateStep 'apply' @('-root', $happy, 'apply', '-msi', $msi, '-backup', $happyBackup) 0
Invoke-MigrateStep 'health' @('-root', $happy, 'health') 0

$inverse = Join-Path $base 'inverse'
$inverseBackup = Join-Path $base 'backup-inverse'
Invoke-MigrateStep 'init-inverse' @('-root', $inverse, 'init', '-scenario', 'happy') 0
Invoke-MigrateStep 'backup-inverse' @('-root', $inverse, 'backup', '-out', $inverseBackup) 0
Invoke-MigrateStep 'apply-fail' @('-root', $inverse, 'apply', '-msi', $msi, '-backup', $inverseBackup, '-fail-after', 'copy') 1
$fx = Get-Content (Join-Path $inverse 'fixture.json') -Raw | ConvertFrom-Json
$alice = $fx.users | Where-Object { $_.name -eq 'alice' }
$pilot = $alice.tasks | Where-Object { $_.name -eq 'alice-pilot' }
if (-not $pilot.enabled) { throw 'inverse did not restore the pilot task' }
$proc = $alice.processes | Select-Object -First 1
if (-not $proc.running) { throw 'inverse did not restore the owned process' }
if ($fx.service.exists) { throw 'inverse left the product service recorded' }
$copied = Join-Path $inverse 'users/alice/local/winunitd/units/fixture.service'
if (Test-Path $copied) { throw 'inverse left a copied unit' }
if (-not (Test-Path (Join-Path $inverseBackup 'manifest.json'))) { throw 'backup was not retained' }

$conflict = Join-Path $base 'conflict'
Invoke-MigrateStep 'init-conflict' @('-root', $conflict, 'init', '-scenario', 'custom-base-dir') 0
$before = (Get-FileHash (Join-Path $conflict 'fixture.json')).Hash
Invoke-MigrateStep 'dry-run-conflict' @('-root', $conflict, 'dry-run', '-msi', $msi) 2
$after = (Get-FileHash (Join-Path $conflict 'fixture.json')).Hash
if ($before -ne $after) { throw 'conflict dry-run mutated the fixture' }

Write-Output 'r65-fixture-pass'
