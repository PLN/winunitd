param(
	[Parameter(Mandatory=$true)][string]$Config,
	[ValidateSet('Plan','Status','Rehearse','FailAfterStop','Update','Recover')][string]$Action = 'Plan',
	[ValidateRange(0,120)][int]$DelaySeconds = 0,
	[switch]$Worker,
	[switch]$Library
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Write-Json($Path, $Value) {
	$temp = $Path + '.new'
	$bytes = [Text.Encoding]::UTF8.GetBytes(($Value | ConvertTo-Json -Depth 12))
	$f = [IO.File]::Open($temp, 'Create', 'Write', 'None')
	try { $f.Write($bytes,0,$bytes.Length); $f.Flush($true) } finally { $f.Dispose() }
	if (Test-Path -LiteralPath $Path) { [IO.File]::Replace($temp, $Path, [NullString]::Value) }
	else { [IO.File]::Move($temp, $Path) }
}
function Get-LatestResult($StateDir) {
	Get-ChildItem -LiteralPath $StateDir -Directory | Where-Object Name -match '^[a-f0-9]{32}$' | ForEach-Object {
		$path = Join-Path $_.FullName 'result.json'
		if (Test-Path -LiteralPath $path) { Get-Item -LiteralPath $path }
	} | Sort-Object LastWriteTime -Descending | Select-Object -First 1
}
function Invoke-Native($Exe, [string[]]$Arguments, $Log, [int]$Seconds = 180) {
	# All arguments supplied by this script/config are simple paths or switches.
	foreach ($arg in $Arguments) { if ($arg.Contains('"') -or $arg.Contains("`n")) { throw 'Invalid native argument' } }
	$quoted = ($Arguments | ForEach-Object { '"' + $_ + '"' }) -join ' '
	$p = Start-Process -FilePath $Exe -ArgumentList $quoted -WindowStyle Hidden -PassThru -RedirectStandardOutput $Log -RedirectStandardError ($Log + '.err')
	$null = $p.Handle
	if (-not $p.WaitForExit($Seconds * 1000)) {
		& "$env:SystemRoot\System32\taskkill.exe" /PID $p.Id /T /F | Out-Null
		throw 'Native maintenance operation timed out; inspect retained logs'
	}
	$p.WaitForExit()
	$p.Refresh()
	return $p.ExitCode
}
function Assert-Config($c) {
	foreach ($name in 'PilotRoot','HermesHome','ProjectRoot','Python','StateDir') {
		if (-not [IO.Path]::IsPathRooted($c.$name) -or $c.$name.Contains('"')) { throw "Invalid $name" }
	}
	if ($c.LegacyTasks.Count -ne 3 -or $c.Units.Count -ne 3) { throw 'Expected three pilot components' }
	if (@($c.LegacyTasks + @($c.PilotTask,$c.MaintenanceTask) | Select-Object -Unique).Count -ne 5) { throw 'Task identities must be distinct' }
	foreach ($task in $c.LegacyTasks + @($c.PilotTask,$c.MaintenanceTask)) {
		if ($task -notmatch '^[A-Za-z0-9_-]+$' -or $task -eq 'Schedule') { throw 'Invalid task identity' }
	}
	if (-not $c.ProjectRoot.StartsWith($c.HermesHome.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Project must be inside the configured home' }
	if ($c.StateDir.StartsWith($c.HermesHome, [StringComparison]::OrdinalIgnoreCase)) { throw 'Backups must be outside Hermes home' }
}
function Assert-OutsidePilot($c) {
	$procs = @(Get-CimInstance Win32_Process)
	$current = $PID
	$seen = @{}
	while ($current -gt 0 -and -not $seen.ContainsKey($current)) {
		$seen[$current] = $true
		$p = $procs | Where-Object ProcessId -eq $current | Select-Object -First 1
		if (-not $p) { break }
		if ($p.ExecutablePath -eq (Join-Path $c.PilotRoot 'bin\winunitd.exe') -or ($p.CommandLine -and $p.CommandLine.Contains($c.HermesHome))) { throw 'Maintenance must run outside the Hermes/pilot process tree' }
		$current = [int]$p.ParentProcessId
	}
}
function Assert-NoRuntimes($c) {
	$holders = @(Get-CimInstance Win32_Process | Where-Object {
		$_.ProcessId -ne $PID -and $_.CommandLine -and
		($_.CommandLine.IndexOf($c.HermesHome, [StringComparison]::OrdinalIgnoreCase) -ge 0 -or
		 $_.CommandLine.IndexOf($c.WebUIHome, [StringComparison]::OrdinalIgnoreCase) -ge 0)
	})
	if ($holders.Count) { throw ('Hermes file/runtime holders remain; PIDs: ' + (($holders.ProcessId) -join ',')) }
	foreach ($port in $c.Ports) {
		if (Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue) { throw "Workload listener remains on port $port" }
	}
}
function Assert-NoAutostart($c) {
	$unexpected = @(Get-ScheduledTask | Where-Object { $_.TaskName -like 'Hermes*' })
	if ($unexpected.Count) { throw 'Hermes task registrations remain; refusing updater cold-start risk' }
	foreach ($folder in @([Environment]::GetFolderPath('Startup'), [Environment]::GetFolderPath('CommonStartup'))) {
		if ($folder -and @(Get-ChildItem -LiteralPath $folder -Filter '*hermes*' -ErrorAction SilentlyContinue).Count) { throw 'Hermes Startup entry must be handled before maintenance' }
	}
}
function Save-TaskSnapshot($c, $run) {
	$pilot = Get-ScheduledTask -TaskName $c.PilotTask
	if ($pilot.State -ne 'Running' -or -not $pilot.Settings.Enabled) { throw 'Pilot must be enabled and running before this procedure' }
	foreach ($name in $c.LegacyTasks) {
		$t = Get-ScheduledTask -TaskName $name
		if ($t.State -ne 'Disabled') { throw 'Legacy tasks must already be disabled' }
	}
	foreach ($name in $c.LegacyTasks + @($c.PilotTask)) {
		[IO.File]::WriteAllText((Join-Path $run ($name + '.xml')), (Export-ScheduledTask -TaskName $name), [Text.Encoding]::Unicode)
	}
	Write-Json (Join-Path $run 'baseline.json') @{
		binaries=@(Get-ChildItem -LiteralPath (Join-Path $c.PilotRoot 'bin') -Filter '*.exe' | Get-FileHash -Algorithm SHA256 | Select-Object Path,Hash)
		schedulerPid=(Get-CimInstance Win32_Service -Filter "Name='Schedule'").ProcessId
	}
	$git = (Get-Command git.exe).Source
	if ((Invoke-Native $git @('-C',$c.ProjectRoot,'rev-parse','HEAD') (Join-Path $run 'source-before.txt')) -ne 0) { throw 'Cannot record source revision' }
	if ((Invoke-Native $git @('-C',$c.ProjectRoot,'status','--short') (Join-Path $run 'changes-before.txt')) -ne 0) { throw 'Cannot record source changes' }
}
function Stop-Pilot($c, $run) {
	Disable-ScheduledTask -TaskName $c.PilotTask | Out-Null
	foreach ($unit in $c.Units) {
		$exitCode = Invoke-Native (Join-Path $c.PilotRoot 'bin\winctl.exe') @('--user','stop',$unit) (Join-Path $run ($unit + '-stop.log')) 45
		if ($exitCode -ne 0) { throw "Could not stop $unit" }
	}
	Stop-ScheduledTask -TaskName $c.PilotTask
	$exe = Join-Path $c.PilotRoot 'bin\winunitd.exe'
	Get-CimInstance Win32_Process | Where-Object ExecutablePath -eq $exe | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
	Start-Sleep -Seconds 2
	Assert-NoRuntimes $c
}
function Remove-LegacyTasks($c) {
	foreach ($name in $c.LegacyTasks) { Unregister-ScheduledTask -TaskName $name -Confirm:$false }
	Assert-NoAutostart $c
}
function Restore-Tasks($c, $run) {
	foreach ($name in $c.LegacyTasks) {
		[xml]$xml = [IO.File]::ReadAllText((Join-Path $run ($name + '.xml')))
		$xml.Task.Settings.Enabled = 'false'
		Register-ScheduledTask -TaskName $name -Xml $xml.OuterXml -Force | Out-Null
		if ((Get-ScheduledTask -TaskName $name).State -ne 'Disabled') { throw 'Restored legacy task was not disabled' }
	}
}
function Start-Pilot($c, $run) {
	Enable-ScheduledTask -TaskName $c.PilotTask | Out-Null
	Start-ScheduledTask -TaskName $c.PilotTask
	$deadline = (Get-Date).AddSeconds(300)
	do {
		Start-Sleep -Seconds 3
		$ready = $true
		foreach ($port in $c.Ports) { if (-not (Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue)) { $ready = $false } }
		if ($ready) { break }
	} while ((Get-Date) -lt $deadline)
	if (-not $ready) { throw 'Pilot listener recovery timed out' }
	$exitCode = Invoke-Native (Join-Path $c.PilotRoot 'bin\winctl.exe') @('--user','list-units') (Join-Path $run 'units-after.log')
	if ($exitCode -ne 0) { throw 'Pilot control did not recover' }
	$units = [IO.File]::ReadAllText((Join-Path $run 'units-after.log'))
	foreach ($unit in $c.Units) { if ($units -notmatch ([regex]::Escape($unit) + '\s+loaded\s+active\s')) { throw "Unit failed recovery: $unit" } }
	$managers = @(Get-CimInstance Win32_Process | Where-Object ExecutablePath -eq (Join-Path $c.PilotRoot 'bin\winunitd.exe'))
	if ($managers.Count -ne 1) { throw 'Expected exactly one pilot manager' }
	$all = @(Get-CimInstance Win32_Process)
	foreach ($port in $c.Ports) {
		foreach ($listener in @(Get-NetTCPConnection -State Listen -LocalPort $port)) {
			$cursor = [int]$listener.OwningProcess
			$seen = @{}
			while ($cursor -ne $managers[0].ProcessId -and $cursor -gt 0 -and -not $seen.ContainsKey($cursor)) {
				$seen[$cursor] = $true
				$p = $all | Where-Object ProcessId -eq $cursor | Select-Object -First 1
				if (-not $p) { break }
				$cursor = [int]$p.ParentProcessId
			}
			if ($cursor -ne $managers[0].ProcessId) { throw 'A listener escaped pilot ownership' }
		}
	}
	if ((Get-Service Schedule).Status -ne 'Running') { throw 'Task Scheduler is not running' }
	$baseline = Get-Content -Raw -LiteralPath (Join-Path $run 'baseline.json') | ConvertFrom-Json
	if ((Get-CimInstance Win32_Service -Filter "Name='Schedule'").ProcessId -ne $baseline.schedulerPid) { throw 'Task Scheduler process changed during maintenance' }
	$git = (Get-Command git.exe).Source
	if ((Invoke-Native $git @('-C',$c.ProjectRoot,'rev-parse','HEAD') (Join-Path $run 'source-after.txt')) -ne 0) { throw 'Cannot record resulting revision' }
}
function Backup-Home($c, $run) {
	$target = Join-Path $run 'backup-home'
	$exitCode = Invoke-Native "$env:SystemRoot\System32\robocopy.exe" @($c.HermesHome,$target,'/E','/XJ','/COPY:DAT','/DCOPY:DAT','/R:1','/W:1','/NP','/NFL','/NDL','/XD',(Join-Path $c.HermesHome 'backups')) (Join-Path $run 'backup.log') 1800
	if ($exitCode -ge 8) { throw 'Hermes backup failed' }
}
function Invoke-Update($c, $run, [bool]$Apply) {
	$env:HERMES_HOME = $c.HermesHome
	$env:VIRTUAL_ENV = Join-Path $c.ProjectRoot 'venv'
	$env:PYTHONPATH = $c.ProjectRoot + ';' + (Join-Path $c.ProjectRoot 'venv\Lib\site-packages')
	$env:PYTHONIOENCODING = 'utf-8'
	$env:PYTHONUNBUFFERED = '1'
	$args = @('-m','hermes_cli.main','update')
	if ($Apply) { $args += @('--yes','--backup') } else { $args += '--plan' }
	Push-Location $c.HermesHome
	try { $exitCode = Invoke-Native $c.Python $args (Join-Path $run 'updater.log') 2400 }
	finally { Pop-Location }
	if ($exitCode -ne 0) { throw "Hermes updater exited $exitCode; retained maintenance state requires inspection" }
	Assert-NoRuntimes $c
	Assert-NoAutostart $c
}
function Invoke-Transaction($c, $run, $mode) {
	$state = [ordered]@{schema=1; action=$mode; phase='preflight'; mutationStarted=$false; recoveryRequired=$false; success=$false; started=(Get-Date).ToUniversalTime().ToString('o')}
	$statePath = Join-Path $run 'result.json'
	$stopped = $false
	$snapshot = $false
	try {
		Write-Json $statePath $state
		Assert-OutsidePilot $c
		Save-TaskSnapshot $c $run
		$snapshot = $true
		$state.phase = 'stopping'; Write-Json $statePath $state
		$stopped = $true
		Stop-Pilot $c $run
		Remove-LegacyTasks $c
		$state.phase = 'quiescent'; Write-Json $statePath $state
		if ($mode -eq 'FailAfterStop') { throw 'Injected failure after quiescence' }
		if ($mode -eq 'Update') {
			$state.phase = 'backup'; Write-Json $statePath $state
			Backup-Home $c $run
			$state.phase = 'updating'; $state.mutationStarted = $true; Write-Json $statePath $state
			Invoke-Update $c $run $true
		} else { Invoke-Update $c $run $false }
		$state.phase = 'restoring'; Write-Json $statePath $state
		Restore-Tasks $c $run
		Start-Pilot $c $run
		$stopped = $false
		$state.phase = 'completed'; $state.success = $true
	} catch {
		$state.phase = 'failed'; $state.error = $_.Exception.Message
		if ($stopped -and $snapshot -and -not $state.mutationStarted) {
			$state.phase = 'recovering'; Write-Json $statePath $state
			try { Restore-Tasks $c $run; Start-Pilot $c $run; $stopped = $false; $state.phase = 'failed-recovered' }
			catch { $state.phase = 'failed-recovery'; $state.recoveryError = $_.Exception.Message }
		}
		$state.recoveryRequired = $stopped
	} finally {
		$state.completed = (Get-Date).ToUniversalTime().ToString('o')
		Write-Json $statePath $state
	}
	return $state
}
if ($Library) { return }
$c = Get-Content -Raw -LiteralPath $Config | ConvertFrom-Json
Assert-Config $c
New-Item -ItemType Directory -Path $c.StateDir -Force | Out-Null
$requestPath = Join-Path $c.StateDir 'request.json'
if ($Worker) {
	$lock = [IO.File]::Open((Join-Path $c.StateDir 'worker.lock'), 'OpenOrCreate', 'ReadWrite', 'None')
	try {
		$request = Get-Content -Raw -LiteralPath $requestPath | ConvertFrom-Json
		if ($request.action -notin @('Rehearse','FailAfterStop','Update','Recover') -or $request.id -notmatch '^[a-f0-9]{32}$') { throw 'Invalid maintenance request' }
		$run = Join-Path $c.StateDir $request.id
		if ($request.action -eq 'Recover') {
			$statePath = Join-Path $run 'result.json'
			$prior = Get-Content -Raw -LiteralPath $statePath | ConvertFrom-Json
			if ($prior.mutationStarted) { throw 'Update mutation started; inspect installed state and backup before manual recovery' }
			Assert-OutsidePilot $c
			Restore-Tasks $c $run
			Start-Pilot $c $run
			$prior.phase = 'manually-recovered'; $prior.recoveryRequired = $false
			Write-Json $statePath $prior
			Remove-Item -LiteralPath $requestPath
			return
		}
		if (Test-Path -LiteralPath $run) { throw 'Request already consumed; inspect its result before recovery' }
		New-Item -ItemType Directory -Path $run | Out-Null
		Copy-Item -LiteralPath $Config -Destination (Join-Path $run 'config.json')
		Copy-Item -LiteralPath $PSCommandPath -Destination (Join-Path $run 'worker.ps1')
		if ($request.delaySeconds -lt 0 -or $request.delaySeconds -gt 120) { throw 'Invalid maintenance delay' }
		Write-Json (Join-Path $run 'result.json') @{schema=1; action=$request.action; phase='waiting'; mutationStarted=$false; recoveryRequired=$false; success=$false}
		Start-Sleep -Seconds $request.delaySeconds
		$result = Invoke-Transaction $c $run $request.action
		if (-not $result.recoveryRequired) { Remove-Item -LiteralPath $requestPath }
		if (-not $result.success) { exit 1 }
	} finally { $lock.Dispose() }
	return
}
if ($Action -eq 'Plan') {
	[pscustomobject]@{Pilot=(Get-ScheduledTask -TaskName $c.PilotTask).State; Maintenance=(Get-ScheduledTask -TaskName $c.MaintenanceTask).State; Pending=(Test-Path -LiteralPath $requestPath); StateDir=$c.StateDir}
	return
}
if ($Action -eq 'Status') {
	Get-LatestResult $c.StateDir | ForEach-Object { Get-Content -LiteralPath $_.FullName }
	return
}
$task = Get-ScheduledTask -TaskName $c.MaintenanceTask
if ($task.State -eq 'Running') { throw 'Maintenance already running' }
if ($Action -eq 'Recover') {
	$request = Get-Content -Raw -LiteralPath $requestPath | ConvertFrom-Json
	$request.action = 'Recover'
	Write-Json $requestPath $request
	Start-ScheduledTask -TaskName $c.MaintenanceTask
	Write-Output 'Queued recovery of the interrupted pre-update transaction.'
	return
}
$request = @{schema=1; id=[guid]::NewGuid().ToString('N'); action=$Action; delaySeconds=$DelaySeconds; queued=(Get-Date).ToUniversalTime().ToString('o')}
$f = [IO.File]::Open($requestPath, 'CreateNew', 'Write', 'None')
try { $bytes = [Text.Encoding]::UTF8.GetBytes(($request | ConvertTo-Json)); $f.Write($bytes,0,$bytes.Length); $f.Flush($true) } finally { $f.Dispose() }
Start-ScheduledTask -TaskName $c.MaintenanceTask
Write-Output ('Queued ' + $Action + ': ' + $request.id + '. This process may exit; the scheduled maintenance task owns execution.')
