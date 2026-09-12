$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'hermes-unit.ps1') -Config unused -Library
$root=Join-Path ([IO.Path]::GetTempPath()) ('unit-maintenance-test-'+[guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $root | Out-Null
function Assert($condition,$message) { if (-not $condition) { throw $message } }
function Step($name) { $script:steps.Add($name);if ($script:fail -eq $name) { throw "injected $name" } }
function Assert-UnitWorker($c) { Step 'identity' }
function Save-UnitSnapshot($c,$run) { Step 'snapshot' }
function Suspend-UnitWorkloads($c,$run) { Step 'hold-stop' }
function Resume-UnitWorkloads($c,$run) { Step 'restore' }
function Backup-Home($c,$run) { Step 'backup' }
function Invoke-Update($c,$run,$apply) { if ($apply) { Step 'update' } else { Step 'plan' } }
function Invoke-UnitControl($c,$arguments,$run,$label) { Step $label }
foreach ($case in @(
    @{mode='Rehearse';fail='';phase='completed';hold=$false;sequence='identity,snapshot,hold-stop,plan,restore'},
    @{mode='Update';fail='';phase='completed';hold=$false;sequence='identity,snapshot,hold-stop,backup,update,restore'},
    @{mode='FailAfterStop';fail='';phase='failed-recovered';hold=$false;sequence='identity,snapshot,hold-stop,restore'},
    @{mode='Update';fail='snapshot';phase='failed';hold=$false;sequence='identity,snapshot'},
    @{mode='Update';fail='hold-stop';phase='failed-recovered';hold=$false;sequence='identity,snapshot,hold-stop,restore'},
    @{mode='Update';fail='backup';phase='failed-recovered';hold=$false;sequence='identity,snapshot,hold-stop,backup,restore'},
    @{mode='Rehearse';fail='plan';phase='failed-recovered';hold=$false;sequence='identity,snapshot,hold-stop,plan,restore'},
    @{mode='Rehearse';fail='restore';phase='failed-recovery';hold=$true;sequence='identity,snapshot,hold-stop,plan,restore,restore,retain-hold,hold-stop'},
    @{mode='Update';fail='update';phase='failed';hold=$true;sequence='identity,snapshot,hold-stop,backup,update,retain-hold,hold-stop'},
    @{mode='Update';fail='restore';phase='failed';hold=$true;sequence='identity,snapshot,hold-stop,backup,update,restore,retain-hold,hold-stop'}
)) {
    $script:steps=[Collections.Generic.List[string]]::new();$script:fail=$case.fail
    $run=Join-Path $root ([guid]::NewGuid().ToString('N'));New-Item -ItemType Directory -Path $run | Out-Null
    $r=Invoke-UnitTransaction @{TargetUnit='hermes.target'} $run $case.mode
    Assert ($r.phase -eq $case.phase) "Wrong phase: $($case.mode)/$($case.fail): $($r.phase)"
    Assert ($r.recoveryRequired -eq $case.hold) 'Wrong persistent hold outcome'
    Assert (($script:steps -join ',') -eq $case.sequence) "Wrong sequence: $($script:steps -join ',')"
    $disk=Get-Content -LiteralPath (Join-Path $run 'result.json') -Raw | ConvertFrom-Json
    Assert ($disk.phase -eq $r.phase -and $disk.recoveryRequired -eq $r.recoveryRequired) 'Durable result mismatch'
}
Write-Output 'PASS: unit maintenance ordering, boot hold, pre-mutation recovery, post-mutation hold and durable results.'
