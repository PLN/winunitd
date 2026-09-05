param(
	[Parameter(Mandatory)][string]$PackageDirectory,
	[Parameter(Mandatory)][string]$EvidenceDirectory,
	[Parameter(Mandatory)][switch]$DisposableLab,
	[switch]$QualifyHelper
)
$ErrorActionPreference = 'Stop'
if (-not $DisposableLab) { throw 'Disposable lab acknowledgement required' }
$name = 'winunitd-msi-fixture'
if (Get-Service $name -ErrorAction SilentlyContinue) { throw 'Fixture already exists; preserve it for inspection' }
$packages = (Resolve-Path $PackageDirectory).Path
New-Item -ItemType Directory -Force $EvidenceDirectory | Out-Null
$evidence = (Resolve-Path $EvidenceDirectory).Path
$installed = Join-Path $env:ProgramFiles $name
function Invoke-Msi([string]$Phase, [string[]]$Arguments) {
	$timer = [Diagnostics.Stopwatch]::StartNew()
	$transaction = [guid]::NewGuid().ToString('N')
	$p = Start-Process msiexec.exe -ArgumentList ($Arguments + @("FIXTURE_TRANSACTION=$transaction", '/qn', '/norestart', '/L*v', "`"$evidence\$Phase.log`"")) -WindowStyle Hidden -PassThru
	if (-not $p.WaitForExit(300000)) { throw "MSI timeout in $Phase; preserve process and logs for inspection" }
	$p.Refresh()
	$result = [ordered]@{phase=$Phase; exit_code=$p.ExitCode; seconds=[math]::Round($timer.Elapsed.TotalSeconds, 2)}
	$result | ConvertTo-Json -Compress | Add-Content "$evidence\results.jsonl"
	Write-Output ($result | ConvertTo-Json -Compress)
	if ($QualifyHelper) {
		$expected = if ($Phase -like '*rollback*') { 1603 } else { 0 }
		if ($p.ExitCode -ne $expected) { throw "Unexpected exit code in $Phase" }
		if ($Phase -eq 'slow-upgrade' -and $timer.Elapsed.TotalSeconds -lt 40) { throw 'Slow stop was not exercised' }
		if ($Phase -eq 'deadline-rollback' -and $timer.Elapsed.TotalSeconds -lt 175) { throw 'Stop deadline was not exercised' }
	}
}
function Capture-State([string]$Phase) {
	foreach ($query in @('queryex', 'qc', 'qfailure', 'qfailureflag', 'qprivs', 'qsidtype', 'qtriggerinfo')) {
		& sc.exe $query $name | Out-File "$evidence\$Phase-$query.txt"
	}
	$service = Get-CimInstance Win32_Service -Filter "Name='$name'"
	$state = [ordered]@{phase=$Phase; state=$service.State; process_id=$service.ProcessId; start_name=$service.StartName}
	if (Test-Path "$installed\fixture.exe") { $state.sha256 = (Get-FileHash "$installed\fixture.exe").Hash.ToLowerInvariant() }
	$reg = Get-ItemProperty "HKLM:\SYSTEM\CurrentControlSet\Services\$name" -ErrorAction SilentlyContinue
	$state.delayed_auto_start = $reg.DelayedAutostart
	$state.preshutdown_timeout = $reg.PreshutdownTimeout
	$state.non_crash_recovery = $reg.FailureActionsOnNonCrashFailures
	$state.recovery_reset_seconds = if ($reg.FailureActions) { [BitConverter]::ToUInt32($reg.FailureActions, 0) } else { $null }
	$state | ConvertTo-Json -Compress | Add-Content "$evidence\states.jsonl"
	Write-Output ($state | ConvertTo-Json -Compress)
	if (Test-Path "$installed\events.jsonl") { Copy-Item "$installed\events.jsonl" "$evidence\$Phase-events.jsonl" }
	if ($QualifyHelper) {
		if ($Phase -eq 'uninstall') {
			if ($service -or (Test-Path "$installed\fixture.exe")) { throw 'Uninstall left fixture ownership behind' }
		} else {
			$expectedState = if ($Phase -eq 'stopped-rollback') { 'Stopped' } else { 'Running' }
			if ($state.state -ne $expectedState -or $state.start_name -ne 'LocalSystem' -or $state.delayed_auto_start -ne 1 -or $state.preshutdown_timeout -ne 180000 -or $state.non_crash_recovery -ne 1 -or $state.recovery_reset_seconds -ne [uint32]::MaxValue) { throw "Service state mismatch after $Phase" }
			if ($Phase -like '*rollback*' -and $state.sha256 -ne $script:originalHash) { throw 'Rollback did not restore original bytes' }
		}
	}
}
Invoke-Msi 'install' @('/i', "`"$packages\fixture-0.0.1.msi`"")
Capture-State 'install'
$script:originalHash = (Get-FileHash "$installed\fixture.exe").Hash.ToLowerInvariant()
if (-not (Get-Service $name -ErrorAction SilentlyContinue)) { throw 'Installation did not create fixture; inspect MSI log' }
Invoke-Msi 'repair' @('/i', "`"$packages\fixture-0.0.1.msi`"", 'REINSTALL=ALL', 'REINSTALLMODE=amus')
Capture-State 'repair'
Invoke-Msi 'rollback' @('/i', "`"$packages\fixture-0.0.2.msi`"", 'FIXTURE_FAIL=1')
Capture-State 'rollback'
if ($QualifyHelper) {
	& sc.exe stop $name | Out-Null
	(Get-Service $name).WaitForStatus('Stopped', [timespan]::FromSeconds(30))
	Invoke-Msi 'stopped-rollback' @('/i', "`"$packages\fixture-0.0.2.msi`"", 'FIXTURE_FAIL=1')
	Capture-State 'stopped-rollback'
	Start-Service $name
	Set-Content "$installed\stop-seconds" '200'
	Invoke-Msi 'deadline-rollback' @('/i', "`"$packages\fixture-0.0.2.msi`"")
	Capture-State 'deadline-rollback'
}
Set-Content "$installed\stop-seconds" '45'
Invoke-Msi 'slow-upgrade' @('/i', "`"$packages\fixture-0.0.2.msi`"")
Capture-State 'slow-upgrade'
# Allow the fixture's bounded slow stop to complete before further observation.
if (-not $QualifyHelper) { Start-Sleep -Seconds 50 }
Capture-State 'slow-upgrade-settled'
Set-Content "$installed\stop-seconds" '0'
Invoke-Msi 'uninstall' @('/x', "`"$packages\fixture-0.0.2.msi`"")
Capture-State 'uninstall'
