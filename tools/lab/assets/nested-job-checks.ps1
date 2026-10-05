<#
Installed-daemon lane for workload-created nested jobs (#265): N06 crashes
the headless user manager, N07 crashes the broker. Runs one execution as
SYSTEM on a disposable machine with the admitted product installed and
records one result for the case matrix.

The native fixture does the process work: its observer holds every old
process, terminates the exact crash target, waits for the replacement
generation, records kernel exit and creation times of the raw identities
and confirms cleanup. Its record step recomputes the verdict from those
identities and binds the result to the case, mode, repetition, expected
account and admitted run. This script checks the admitted build, renders
and removes the unit, checks the daemon and records. Machine-specific
inputs (paths, the dedicated headless account and its manager directory)
are parameters; nothing environment-specific is built in.

Admission: -Admission names the controller's reviewed run manifest (schema
1, a full clean source commit and the SHA-256 of every admitted executable).
Before anything is installed or crashed, the installed winunitd.exe and
winctl.exe and the fixture must match its artifacts named winunitd.exe,
winctl.exe and nested-job.exe; the fixture checks the manifest again and
records its hash in the result.

Evidence: the transcript (driver.log), the observer report and the case
directory contain machine paths, account names and SIDs. They are private
qualification evidence: never publish them as CI artifacts or attach them
to issues or pull requests. The result record's detail is a summary with
those values replaced by placeholders.

Preconditions owned by the caller: the account for headless cases is a
dedicated, local standard account with linger enabled and no interactive
logon; the fixture is readable and executable by that account; the case
root is fresh. The script leaves linger enabled for that account.

How a headless unit is installed (for review with the lab's account
conventions): an administrator cannot reach another account's manager
pipe, so the script places the unit itself and restarts that manager.
  1. It writes the unit file to <HeadlessBase>\units and the enable link
     <HeadlessBase>\enabled\default.target\<unit> (content: the unit name),
     the same files winctl enable writes. Both inherit the ACL of the
     account's manager directory; their owner is the SYSTEM caller.
  2. It runs winctl disable-linger <account>, which stops the account's
     manager and its units, waits until no manager process for the SID
     remains, then runs winctl enable-linger <account>. The broker starts a
     new S4U manager, which boots enabled units, including this one.
  3. Teardown removes both files, repeats disable-linger and enable-linger,
     and so leaves linger enabled with the unit gone.
Side effects: every unit of that account restarts twice per execution, and
the linger record is rewritten. Use an account that hosts nothing else.
#>
[CmdletBinding()]
param(
	[Parameter(Mandatory)][ValidateSet('N06', 'N07')][string]$Case,
	[Parameter(Mandatory)][ValidateSet('assign', 'job-list')][string]$Mode,
	[Parameter(Mandatory)][ValidateSet('system', 'headless')][string]$Identity,
	[Parameter(Mandatory)][ValidatePattern('^r[1-9]$')][string]$Repetition,
	[Parameter(Mandatory)][string]$Fixture,
	[Parameter(Mandatory)][string]$Admission,
	[Parameter(Mandatory)][string]$CaseRoot,
	[Parameter(Mandatory)][string]$Results,
	[string]$InstallDir = (Join-Path $env:ProgramFiles 'winunitd\bin'),
	[string]$DataDir = (Join-Path $env:ProgramData 'winunitd'),
	[ValidatePattern('^(|S-1-5-21-\d+-\d+-\d+-\d+)$')][string]$HeadlessSid = '',
	[ValidatePattern('^[A-Za-z0-9._-]{0,64}$')][string]$HeadlessAccount = '',
	[string]$HeadlessBase = '',
	[ValidateRange(30, 600)][int]$TimeoutSeconds = 180,
	[switch]$DisposableLab
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

#region pure functions: parameters in, values out, no machine state
# Parameter rules. The headless account, its name and its manager directory
# are required only for headless cases; SYSTEM cases ignore them.
function Test-NestedJobParameters {
	param([string]$Case, [string]$Identity, [string]$HeadlessSid, [string]$HeadlessAccount, [string]$HeadlessBase, [string[]]$Paths)
	if ($Case -eq 'N06' -and $Identity -ne 'headless') { throw 'N06 has no SYSTEM variant' }
	$headless = $Identity -eq 'headless'
	if ($headless -and (!$HeadlessSid -or !$HeadlessAccount -or !$HeadlessBase)) { throw 'Headless cases need the account SID, name and manager directory' }
	foreach ($path in @($Paths) + @($(if ($headless) { $HeadlessBase }))) {
		if (!$path -or ![IO.Path]::IsPathRooted($path) -or $path -match '"') { throw 'Every path parameter must be absolute' }
	}
}

# The case's unit name, case directory, unit file and, for a headless case
# only, the enable link in the account's manager directory.
function Get-NestedJobLayout {
	param([string]$Case, [string]$Mode, [string]$Identity, [string]$Repetition, [string]$CaseRoot, [string]$DataDir, [string]$HeadlessBase)
	$name = "nested-$($Case.ToLowerInvariant())-$Mode-$Repetition.service"
	$layout = [ordered]@{ Name = $name; CaseDir = (Join-Path $CaseRoot "$($Case.ToLowerInvariant())-$Mode-$Identity-$Repetition"); UnitDir = $null; UnitPath = $null; EnablePath = $null }
	if ($Identity -eq 'headless') {
		$layout.UnitDir = Join-Path $HeadlessBase 'units'
		$layout.EnablePath = Join-Path (Join-Path (Join-Path $HeadlessBase 'enabled') 'default.target') $name
	} else {
		$layout.UnitDir = Join-Path $DataDir 'units'
	}
	$layout.UnitPath = Join-Path $layout.UnitDir $name
	return $layout
}

# Kill a process this driver started and confirm that it exited. A kill
# request is not an exit: an unconfirmed exit is reported, and the case
# directory stays as evidence.
function Stop-ExactProcess([Diagnostics.Process]$Process, [string]$What) {
	try { if (!$Process.HasExited) { $Process.Kill() } } catch { }
	if ($Process.WaitForExit(30000)) { return "$What was killed after its deadline" }
	return "$What did not exit after kill: cleanup unconfirmed, case evidence kept"
}
#endregion

if (!$DisposableLab) { throw 'Explicit disposable-lab acknowledgement required' }
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'Expected SYSTEM' }
Test-NestedJobParameters -Case $Case -Identity $Identity -HeadlessSid $HeadlessSid -HeadlessAccount $HeadlessAccount -HeadlessBase $HeadlessBase `
	-Paths @($Fixture, $Admission, $CaseRoot, $Results, $InstallDir, $DataDir)
$headless = $Identity -eq 'headless'
$layout = Get-NestedJobLayout -Case $Case -Mode $Mode -Identity $Identity -Repetition $Repetition -CaseRoot $CaseRoot -DataDir $DataDir -HeadlessBase $HeadlessBase
$name, $caseDir = $layout.Name, $layout.CaseDir
$expectSid = if ($headless) { $HeadlessSid } else { 'S-1-5-18' }
$daemon = Join-Path $InstallDir 'winunitd.exe'
$winctl = Join-Path $InstallDir 'winctl.exe'

# The admitted build, checked before anything is installed or crashed.
$manifest = Get-Content -LiteralPath $Admission -Raw | ConvertFrom-Json
if ($manifest.schema -ne 1 -or $manifest.source -notmatch '^[0-9a-f]{40}$' -or $manifest.dirty) { throw 'The admission manifest must name one clean, full source commit' }
function Get-AdmittedHash([string]$Name) {
	$found = @($manifest.artifacts | Where-Object { $_.name -eq $Name })
	if ($found.Count -ne 1 -or $found[0].sha256 -notmatch '^[0-9a-f]{64}$') { throw "The admission manifest does not admit $Name" }
	return $found[0].sha256
}
$admitted = @{ $Fixture = Get-AdmittedHash 'nested-job.exe'; $daemon = Get-AdmittedHash 'winunitd.exe'; $winctl = Get-AdmittedHash 'winctl.exe' }
foreach ($file in $admitted.Keys) {
	if ((Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant() -ne $admitted[$file]) { throw "$(Split-Path -Leaf $file) is not the admitted build" }
}
$service = Get-CimInstance Win32_Service -Filter "Name='winunitd'"
if (!$service -or $service.State -ne 'Running' -or $service.PathName -notlike "*$daemon*") { throw 'The installed winunitd service must be running from the install directory' }

# Windows command-line quoting for one argument (CommandLineToArgvW rules).
function ConvertTo-NativeArgument([string]$Value) {
	if ($Value -and $Value -notmatch '[\s"]') { return $Value }
	$escaped = [regex]::Replace($Value, '(\\*)"', '$1$1\"')
	return '"' + [regex]::Replace($escaped, '(\\+)$', '$1$1') + '"'
}

function Invoke-Native {
	param([string]$File, [string[]]$Arguments, [int[]]$Expected = @(0), [int]$TimeoutMs = 120000)
	$start = New-Object Diagnostics.ProcessStartInfo
	$start.FileName = $File
	$start.Arguments = ($Arguments | ForEach-Object { ConvertTo-NativeArgument $_ }) -join ' '
	$start.UseShellExecute = $false
	$start.CreateNoWindow = $true
	$start.RedirectStandardOutput = $true
	$start.RedirectStandardError = $true
	$process = [Diagnostics.Process]::Start($start)
	try {
		$output = $process.StandardOutput.ReadToEndAsync()
		$errors = $process.StandardError.ReadToEndAsync()
		if (!$process.WaitForExit($TimeoutMs)) {
			$state = Stop-ExactProcess $process 'the command'
			throw "Command exceeded its deadline: $(Split-Path -Leaf $File) $($Arguments[0]); $state"
		}
		$text = $output.GetAwaiter().GetResult()
		$errorText = $errors.GetAwaiter().GetResult()
		if ($process.ExitCode -notin $Expected) { throw "Command failed ($($process.ExitCode)): $(Split-Path -Leaf $File) $($Arguments[0]): $errorText" }
		return $text
	} finally { $process.Dispose() }
}

function Get-UserManagerPid {
	$found = @(Get-CimInstance Win32_Process -Filter "Name='winunitd.exe'" | Where-Object { $_.CommandLine -match "--user-manager\s+$([regex]::Escape($HeadlessSid))(\s|$)" })
	if ($found.Count -gt 1) { throw 'More than one user manager for the account' }
	if ($found.Count -eq 1) { return [uint32]$found[0].ProcessId }
	return $null
}

function Wait-Until([scriptblock]$Condition, [string]$What, [int]$Seconds = $TimeoutSeconds) {
	$deadline = (Get-Date).AddSeconds($Seconds)
	while (!(& $Condition)) {
		if ((Get-Date) -ge $deadline) { throw "Timed out waiting for $What" }
		Start-Sleep -Milliseconds 200
	}
}

# The record's detail: driver checks and failures with machine paths, the
# account and its SID replaced by placeholders.
function Protect-Detail([string]$Text) {
	$pairs = @(@($caseDir, '<case>'), @($CaseRoot, '<case-root>'), @($Results, '<results>'), @($InstallDir, '<install>'),
		@($DataDir, '<data>'), @($Fixture, '<fixture>'), @($Admission, '<admission>'))
	if ($headless) { $pairs += @(@($HeadlessBase, '<account-base>'), @($HeadlessSid, '<account-sid>'), @($HeadlessAccount, '<account>')) }
	# Longest first, so a case directory is replaced before its root.
	foreach ($pair in ($pairs | Where-Object { $_[0] } | Sort-Object { $_[0].Length } -Descending)) { $Text = $Text.Replace($pair[0], $pair[1]) }
	return $Text -replace '[\r\n]+', ' '
}

function Read-ObserverStage([string]$Path) {
	try { return (Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json).stage } catch { return $null }
}

$hashesBefore = @{}
foreach ($file in @($daemon, $winctl)) { $hashesBefore[$file] = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash }
$brokerBefore = [uint32]$service.ProcessId
$checks = New-Object Collections.Generic.List[string]
$failures = New-Object Collections.Generic.List[string]
$observer = $null
$unitDir, $unitPath, $enablePath = $layout.UnitDir, $layout.UnitPath, $layout.EnablePath
$report = Join-Path $caseDir 'observer.json'
$finish = Join-Path $caseDir 'driver-finished'
# What the case has changed so far; teardown undoes exactly that.
$caseCreated = $false
$transcript = $false
$unitWritten = $false
$lingerCycled = $false
$systemEnabled = $false
try {
	if (Test-Path -LiteralPath $caseDir) { throw 'Fresh case directory required' }
	New-Item -ItemType Directory -Path $caseDir | Out-Null
	$caseCreated = $true
	New-Item -ItemType Directory -Force -Path $Results | Out-Null
	if ($headless) {
		& icacls.exe $caseDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' "*${HeadlessSid}:(OI)(CI)F" | Out-Null
		if ($LASTEXITCODE) { throw 'Case directory ACL failed' }
	}
	Start-Transcript -LiteralPath (Join-Path $caseDir 'driver.log') | Out-Null
	$transcript = $true
	$exec = ConvertTo-Json -Compress -InputObject @($Fixture, 'main', '--launch-mode', $Mode, '--case-dir', $caseDir, '--generation', 'auto')
	New-Item -ItemType Directory -Force -Path $unitDir | Out-Null
	$unitWritten = $true
	@"
[Unit]
Description=Nested job qualification
StartLimitBurst=0
[Service]
Type=simple
ExecStart=$exec
WorkingDirectory=$caseDir
Restart=always
RestartSec=1s
[Install]
WantedBy=default.target
"@ | Set-Content -LiteralPath $unitPath -Encoding ASCII
	if ($headless) {
		New-Item -ItemType Directory -Force -Path (Split-Path -Parent $enablePath) | Out-Null
		"$name`n" | Set-Content -LiteralPath $enablePath -Encoding ASCII -NoNewline
		# Restart the account's manager so it boots the newly enabled unit.
		$lingerCycled = $true
		Invoke-Native $winctl @('disable-linger', $HeadlessAccount) | Out-Null
		Wait-Until { $null -eq (Get-UserManagerPid) } 'the previous user manager to exit'
		Invoke-Native $winctl @('enable-linger', $HeadlessAccount) | Out-Null
	} else {
		Invoke-Native $winctl @('daemon-reload') | Out-Null
		$systemEnabled = $true
		Invoke-Native $winctl @('enable', $name) | Out-Null
		Invoke-Native $winctl @('start', $name) | Out-Null
	}
	Wait-Until { Test-Path -LiteralPath (Join-Path $caseDir 'gen-0001\report.jsonl') } 'generation 1'

	$holds = @()
	if ($Case -eq 'N06') {
		Wait-Until { $null -ne (Get-UserManagerPid) } 'the user manager'
		$crash = Get-UserManagerPid
	} else {
		$crash = $brokerBefore
		if ($headless) {
			Wait-Until { $null -ne (Get-UserManagerPid) } 'the user manager'
			$holds = @('--hold-pid', [string](Get-UserManagerPid))
		}
	}
	$observeArgs = @('observe', '--case-dir', $caseDir, '--generation', '1', '--crash-pid', [string]$crash,
		'--report', $report, '--finish-file', $finish, '--timeout', "$($TimeoutSeconds)s", '--admission', $Admission,
		'--case', $Case, '--mode', $Mode, '--identity', $Identity, '--repetition', $Repetition, '--expect-sid', $expectSid) + $holds
	$start = New-Object Diagnostics.ProcessStartInfo
	$start.FileName = $Fixture
	$start.Arguments = ($observeArgs | ForEach-Object { ConvertTo-NativeArgument $_ }) -join ' '
	$start.UseShellExecute = $false
	$start.CreateNoWindow = $true
	$observer = [Diagnostics.Process]::Start($start)
	Wait-Until { (Read-ObserverStage $report) -in @('replaced', 'failed') } 'the replacement generation' ($TimeoutSeconds * 2 + 30)
	if ((Read-ObserverStage $report) -ne 'replaced') { $failures.Add('observer failed before replacement') }

	foreach ($file in @($daemon, $winctl)) {
		if ((Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash -ne $hashesBefore[$file]) { $failures.Add("$(Split-Path -Leaf $file) changed") }
	}
	$checks.Add('daemon, CLI and fixture are the admitted build and unchanged')
	$serviceAfter = Get-CimInstance Win32_Service -Filter "Name='winunitd'"
	if ($serviceAfter.State -ne 'Running') { $failures.Add('service not running after recovery') }
	if ($Case -eq 'N07') {
		if ([uint32]$serviceAfter.ProcessId -eq $brokerBefore) { $failures.Add('broker was not replaced') } else { $checks.Add('broker replaced by SCM recovery') }
	} elseif ([uint32]$serviceAfter.ProcessId -ne $brokerBefore) {
		$failures.Add('broker restarted during a user-manager case')
	}
	if ($headless) {
		$snapshot = Invoke-Native $winctl @('snapshot') | ConvertFrom-Json
		$instance = @($snapshot.userHost.instances | Where-Object { $_.sid -eq $HeadlessSid -and $_.state -eq 'running' })
		if ($instance.Count -ne 1 -or $instance[0].mode -ne 'headless-s4u') { $failures.Add('no running headless-s4u manager for the account') } else { $checks.Add('user manager headless-s4u') }
	} else {
		Invoke-Native $winctl @('status', $name) | Out-Null
		$checks.Add('unit active after recovery')
	}
} catch {
	$failures.Add("driver: $($_.Exception.Message)")
} finally {
	# Teardown stops the replacement before the observer confirms cleanup,
	# and undoes only what this case changed.
	try {
		if ($headless -and $unitWritten) {
			Remove-Item -LiteralPath $enablePath, $unitPath -ErrorAction SilentlyContinue
			if ($lingerCycled) {
				Invoke-Native $winctl @('disable-linger', $HeadlessAccount) | Out-Null
				Wait-Until { $null -eq (Get-UserManagerPid) } 'the user manager to stop'
				Invoke-Native $winctl @('enable-linger', $HeadlessAccount) | Out-Null
			}
		} elseif ($unitWritten) {
			if ($systemEnabled) {
				Invoke-Native $winctl @('stop', $name) -Expected @(0, 1) | Out-Null
				Invoke-Native $winctl @('disable', $name) -Expected @(0, 1) | Out-Null
			}
			Remove-Item -LiteralPath $unitPath -ErrorAction SilentlyContinue
			Invoke-Native $winctl @('daemon-reload') | Out-Null
		}
	} catch { $failures.Add("teardown: $($_.Exception.Message)") }
	if ($caseCreated) { New-Item -ItemType File -Path $finish -Force | Out-Null }
	if ($observer) {
		if (!$observer.WaitForExit(($TimeoutSeconds + 30) * 1000)) {
			$failures.Add('observer did not finish: ' + (Stop-ExactProcess $observer 'the observer'))
		}
		$observer.Dispose()
	}
}

$driverResult = if ($failures.Count) { 'fail' } else { 'pass' }
$detail = Protect-Detail ((@($checks) + @($failures)) -join '; ')
if ($detail.Length -gt 500) { $detail = $detail.Substring(0, 500) }
$recordArgs = @('record', '--report', $report, '--results', $Results, '--admission', $Admission, '--case', $Case, '--mode', $Mode,
	'--identity', $Identity, '--repetition', $Repetition, '--expect-sid', $expectSid, '--driver-result', $driverResult, '--detail', $detail)
$code = 1
if ($caseCreated) {
	try {
		Invoke-Native $Fixture $recordArgs | Out-Null
		$code = 0
	} catch { Write-Output "record: $(Protect-Detail $_.Exception.Message)" }
} else {
	Write-Output "no case was run: $detail"
}
if ($transcript) { Stop-Transcript | Out-Null }
exit $code
