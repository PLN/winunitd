<#
Installed-daemon lane for workload-created nested jobs (#265): N06 crashes
the headless user manager, N07 crashes the broker. Runs one execution as
SYSTEM on a disposable machine with the admitted product installed and
records one result for the case matrix.

The native fixture does the process work: its observer holds every old
process, terminates the exact crash target, waits for the replacement
generation, compares kernel exit and creation times and confirms cleanup.
This script renders and removes the unit, checks the daemon and records.
Machine-specific inputs (paths, the dedicated headless account and its
manager directory) are parameters; nothing environment-specific is built in.

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
	[Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{64}$')][string]$FixtureSha256,
	[Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{40}$')][string]$Source,
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

if (!$DisposableLab) { throw 'Explicit disposable-lab acknowledgement required' }
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'Expected SYSTEM' }
if ($Case -eq 'N06' -and $Identity -ne 'headless') { throw 'N06 has no SYSTEM variant' }
$headless = $Identity -eq 'headless'
if ($headless -and (!$HeadlessSid -or !$HeadlessAccount -or !$HeadlessBase)) { throw 'Headless cases need the account SID, name and manager directory' }
foreach ($path in @($Fixture, $CaseRoot, $Results, $InstallDir, $DataDir) + @($(if ($headless) { $HeadlessBase }))) {
	if (![IO.Path]::IsPathRooted($path) -or $path -match '"') { throw "Path must be absolute: $path" }
}
if ((Get-FileHash -LiteralPath $Fixture -Algorithm SHA256).Hash.ToLowerInvariant() -ne $FixtureSha256) { throw 'Fixture hash mismatch' }
$daemon = Join-Path $InstallDir 'winunitd.exe'
$winctl = Join-Path $InstallDir 'winctl.exe'
$service = Get-CimInstance Win32_Service -Filter "Name='winunitd'"
if (!$service -or $service.State -ne 'Running' -or $service.PathName -notlike "*$daemon*") { throw 'The installed winunitd service must be running from the install directory' }

$name = "nested-$($Case.ToLowerInvariant())-$Mode-$Repetition.service"
$caseDir = Join-Path $CaseRoot "$($Case.ToLowerInvariant())-$Mode-$Identity-$Repetition"
if (Test-Path -LiteralPath $caseDir) { throw 'Fresh case directory required' }
New-Item -ItemType Directory -Path $caseDir | Out-Null
New-Item -ItemType Directory -Force -Path $Results | Out-Null
if ($headless) {
	& icacls.exe $caseDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' "*${HeadlessSid}:(OI)(CI)F" | Out-Null
	if ($LASTEXITCODE) { throw 'Case directory ACL failed' }
}
Start-Transcript -LiteralPath (Join-Path $caseDir 'driver.log') | Out-Null

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
		if (!$process.WaitForExit($TimeoutMs)) { $process.Kill(); throw "Command exceeded its deadline: $(Split-Path -Leaf $File) $($Arguments[0])" }
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

function Read-ObserverStage([string]$Path) {
	try { return (Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json).stage } catch { return $null }
}

$hashesBefore = @{}
foreach ($file in @($daemon, $winctl)) { $hashesBefore[$file] = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash }
$brokerBefore = [uint32]$service.ProcessId
$checks = New-Object Collections.Generic.List[string]
$failures = New-Object Collections.Generic.List[string]
$observer = $null
$unitDir = if ($headless) { Join-Path $HeadlessBase 'units' } else { Join-Path $DataDir 'units' }
$unitPath = Join-Path $unitDir $name
$enablePath = Join-Path (Join-Path $HeadlessBase 'enabled\default.target') $name
$report = Join-Path $caseDir 'observer.json'
$finish = Join-Path $caseDir 'driver-finished'
try {
	$exec = ConvertTo-Json -Compress -InputObject @($Fixture, 'main', '--launch-mode', $Mode, '--case-dir', $caseDir, '--generation', 'auto')
	New-Item -ItemType Directory -Force -Path $unitDir | Out-Null
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
		Invoke-Native $winctl @('disable-linger', $HeadlessAccount) | Out-Null
		Wait-Until { $null -eq (Get-UserManagerPid) } 'the previous user manager to exit'
		Invoke-Native $winctl @('enable-linger', $HeadlessAccount) | Out-Null
	} else {
		Invoke-Native $winctl @('daemon-reload') | Out-Null
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
	$observeArgs = @('observe', '--case-dir', $caseDir, '--generation', '1', '--replacement', '2', '--crash-pid', [string]$crash,
		'--report', $report, '--finish-file', $finish, '--timeout', "$($TimeoutSeconds)s") + $holds
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
	$checks.Add('daemon and CLI hashes unchanged')
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
	$failures.Add("driver: $($_.Exception.Message -replace '[\r\n]+', ' ')")
} finally {
	# Teardown stops the replacement before the observer confirms cleanup.
	try {
		if ($headless) {
			Remove-Item -LiteralPath $enablePath, $unitPath -ErrorAction SilentlyContinue
			Invoke-Native $winctl @('disable-linger', $HeadlessAccount) | Out-Null
			Wait-Until { $null -eq (Get-UserManagerPid) } 'the user manager to stop'
			Invoke-Native $winctl @('enable-linger', $HeadlessAccount) | Out-Null
		} else {
			Invoke-Native $winctl @('stop', $name) -Expected @(0, 1) | Out-Null
			Invoke-Native $winctl @('disable', $name) -Expected @(0, 1) | Out-Null
			Remove-Item -LiteralPath $unitPath -ErrorAction SilentlyContinue
			Invoke-Native $winctl @('daemon-reload') | Out-Null
		}
	} catch { $failures.Add("teardown: $($_.Exception.Message -replace '[\r\n]+', ' ')") }
	New-Item -ItemType File -Path $finish -Force | Out-Null
	if ($observer) {
		if (!$observer.WaitForExit(($TimeoutSeconds + 30) * 1000)) { $observer.Kill(); $failures.Add('observer did not finish') }
		$observer.Dispose()
	}
}

$driverResult = if ($failures.Count) { 'fail' } else { 'pass' }
$detail = (@($checks) + @($failures)) -join '; '
if ($detail.Length -gt 500) { $detail = $detail.Substring(0, 500) }
$tokenSource = if ($headless) { 's4u' } else { 'process' }
$recordArgs = @('record', '--report', $report, '--results', $Results, '--case', $Case, '--mode', $Mode, '--identity', $Identity,
	'--repetition', $Repetition, '--source', $Source, '--token-source', $tokenSource, '--driver-result', $driverResult, '--detail', $detail)
$code = 1
try {
	Invoke-Native $Fixture $recordArgs | Out-Null
	$code = 0
} catch { Write-Output "record: $($_.Exception.Message)" }
Stop-Transcript | Out-Null
exit $code
