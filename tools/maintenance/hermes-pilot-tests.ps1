$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'hermes-pilot.ps1') -Config unused -Library
$root = Join-Path ([IO.Path]::GetTempPath()) ('pilot-maintenance-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $root | Out-Null
function Assert($condition, $message) { if (-not $condition) { throw $message } }
function Step($name) {
	$script:steps.Add($name)
	if ($script:fail -eq $name) { throw "injected $name" }
}
function Assert-OutsidePilot($c) { Step 'outside' }
function Save-TaskSnapshot($c,$run) { Step 'snapshot' }
function Stop-Pilot($c,$run) { Step 'stop' }
function Remove-LegacyTasks($c) { Step 'remove' }
function Restore-Tasks($c,$run) { Step 'restore' }
function Start-Pilot($c,$run) { Step 'start' }
function Backup-Home($c,$run) { Step 'backup' }
function Invoke-Update($c,$run,$apply) { if ($apply) { Step 'update' } else { Step 'plan' } }
foreach ($case in @(
	@{mode='Rehearse'; fail=''; phase='completed'; sequence='outside,snapshot,stop,remove,plan,restore,start'},
	@{mode='Update'; fail=''; phase='completed'; sequence='outside,snapshot,stop,remove,backup,update,restore,start'},
	@{mode='FailAfterStop'; fail=''; phase='failed-recovered'; sequence='outside,snapshot,stop,remove,restore,start'},
	@{mode='Update'; fail='outside'; phase='failed'; sequence='outside'},
	@{mode='Update'; fail='snapshot'; phase='failed'; sequence='outside,snapshot'},
	@{mode='Update'; fail='stop'; phase='failed-recovered'; sequence='outside,snapshot,stop,restore,start'},
	@{mode='Update'; fail='remove'; phase='failed-recovered'; sequence='outside,snapshot,stop,remove,restore,start'},
	@{mode='Update'; fail='backup'; phase='failed-recovered'; sequence='outside,snapshot,stop,remove,backup,restore,start'},
	@{mode='Rehearse'; fail='plan'; phase='failed-recovered'; sequence='outside,snapshot,stop,remove,plan,restore,start'},
	@{mode='Rehearse'; fail='restore'; phase='failed-recovery'; sequence='outside,snapshot,stop,remove,plan,restore,restore'},
	@{mode='Update'; fail='update'; phase='failed'; sequence='outside,snapshot,stop,remove,backup,update'},
	@{mode='Update'; fail='restore'; phase='failed'; sequence='outside,snapshot,stop,remove,backup,update,restore'},
	@{mode='Update'; fail='start'; phase='failed'; sequence='outside,snapshot,stop,remove,backup,update,restore,start'}
)) {
	$script:steps = [Collections.Generic.List[string]]::new()
	$script:fail = $case.fail
	$run = Join-Path $root ([guid]::NewGuid().ToString('N'))
	New-Item -ItemType Directory $run | Out-Null
	$r = Invoke-Transaction @{} $run $case.mode
	Assert ($r.phase -eq $case.phase) "Wrong phase for $($case.mode)/$($case.fail): $($r.phase)"
	Assert (($script:steps -join ',') -eq $case.sequence) "Wrong ordering: $($script:steps -join ',')"
	if ($case.fail -in @('update','restore','start')) { Assert $r.recoveryRequired 'Mutation failure must retain maintenance state' }
	$disk = Get-Content -Raw (Join-Path $run 'result.json') | ConvertFrom-Json
	Assert ($disk.phase -eq $r.phase) 'Durable result differs from returned outcome'
}
$nativeLog = Join-Path $root 'native.log'
$code = Invoke-Native "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" @('-NoProfile','-NonInteractive','-Command','exit 7') $nativeLog 10
Assert ($code -eq 7) "Native exit code was not retained: $code"
$latest = Get-LatestResult $root
$nested = Join-Path $latest.DirectoryName 'backup-home\result.json'
New-Item -ItemType Directory (Split-Path $nested) | Out-Null
Write-Json $nested @{phase='unrelated backup artifact'}
(Get-Item $nested).LastWriteTime = (Get-Date).AddDays(1)
Assert ((Get-LatestResult $root).FullName -eq $latest.FullName) 'Status selected a result inside a backup'
Write-Output 'PASS: transaction ordering, pre-mutation recovery, post-mutation hold, durable outcomes, and native exit reporting.'
