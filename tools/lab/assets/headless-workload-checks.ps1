<#
Headless background workload qualification (#266, with the #254 recovery
groups): runs one execution of the case matrix in
internal/runtime/runtimetest/headless/matrix.json as SYSTEM on a disposable
machine with the admitted product installed, and records its records with
`headless-workload record`. The summary recomputes every verdict from the
raw evidence; this script performs the case's actions, collects that
evidence and composes the observation.

Account and source rules, as decided for this lane:
  - A and B are two disposable local standard accounts. A has never been
    used when the cold boot runs; B has an existing, unloaded profile. The
    nested-job lane uses its own accounts, or the guest is restored to the
    verified pre-profile baseline before H01 (the first-use check proves it).
  - The #254 groups (G1-G5) run on A and B only after H01/H02; B01-B04 are
    references to those same-artifact, same-account and same-mode runs.
  - B is the interactive account for G1, G3 wts-B and G4 logoff-B and
    admission-B, after the phase 1 and 2 bare characterization. There is no
    third standard account. The filtered-administrator lane uses a separate
    administrator account (-AdminSid).
  - Every record of one qualification uses one clean reviewed integration
    artifact (the corrected #254 fix, the nested-job and these harnesses):
    -Admission names its run manifest, and the installed winunitd.exe and
    winctl.exe and the fixture must match it before anything runs.

What the script never does: reboot (the controller reboots between the
prepare and collect stages of H01/H02 and H03), store or pass a password
(password logons, WTS sessions and peer evidence come from lab commands
named by parameters), change SCM policy, or publish evidence. Run
-ListPrerequisites to print what a case needs from the lab.

Evidence: the case directory (transcript, observer and probe reports,
observations) holds account SIDs, profile paths and peer addresses. It is
private qualification evidence; never publish it as CI artifacts or attach
it to issues or pull requests. Record details replace those values with
placeholders.

Lab commands (parameters; each must wait for its work and exit 0 only on
success):
  -WtsClient       logon|logoff <account>: a genuine automated interactive
                   (RDP) logon or logoff of the named account; returns once
                   the session exists or is gone.
  -PasswordRunner  <account> <exe> <args...>: runs the command under a
                   password-bearing logon of that account (not S4U) and
                   returns its exit code.
  -SessionRunner   <account> <exe> <args...>: runs the command inside that
                   account's live interactive session and returns its exit
                   code.
Peer files (written by the lab on the isolated peer and copied to the
guest for the case): -PeerReceipt, the echo server's receipt of the nonce;
-PeerAudit, the SMB server's attribution of the access (class, SID, target,
time).
#>
[CmdletBinding()]
param(
	[Parameter(Mandatory)][ValidatePattern('^(G[1-6]|H(0[1-9]|1[0-9]|2[0-2]))$')][string]$Case,
	[Parameter(Mandatory)][ValidatePattern('^[A-Za-z0-9-]{1,32}$')][string]$Variant,
	[ValidatePattern('^(|r[1-9])$')][string]$Repetition = '',
	[ValidatePattern('^(|[a-z][a-z0-9-]{0,31})$')][string]$Control = '',
	[ValidateSet('run', 'baseline', 'prepare', 'collect')][string]$Stage = 'run',
	[ValidateRange(1, 100000)][int]$Sequence = 1,
	[ValidatePattern('^[A-Za-z0-9._-]{1,64}$')][string]$ExecutionId = 'execution',
	[string]$Fixture = '',
	[string]$Admission = '',
	[string]$CaseRoot = '',
	[string]$Results = '',
	[string]$InstallDir = '',
	[string]$DataDir = '',
	[ValidatePattern('^(|S-1-5-21-\d+-\d+-\d+-\d+)$')][string]$SidA = '',
	[ValidatePattern('^(|S-1-5-21-\d+-\d+-\d+-\d+)$')][string]$SidB = '',
	[ValidatePattern('^[A-Za-z0-9._-]{0,64}$')][string]$AccountA = '',
	[ValidatePattern('^[A-Za-z0-9._-]{0,64}$')][string]$AccountB = '',
	[string]$BaseA = '',
	[string]$BaseB = '',
	[ValidatePattern('^(|S-1-5-21-\d+-\d+-\d+-\d+)$')][string]$AdminSid = '',
	[ValidatePattern('^[A-Za-z0-9._-]{0,64}$')][string]$AdminAccount = '',
	[string]$AdminBase = '',
	[string]$WtsClient = '',
	[string]$PasswordRunner = '',
	[string]$SessionRunner = '',
	[ValidatePattern('^(|[A-Za-z0-9.:\[\]-]+:\d{1,5})$')][string]$PeerEcho = '',
	[string]$PeerReceipt = '',
	[ValidatePattern('^(|[A-Za-z0-9.-]+)$')][string]$SmbServer = '',
	[string]$SmbPath = '',
	[ValidatePattern('^(|[0-9a-f]{64})$')][string]$SmbSha256 = '',
	[string]$PeerAudit = '',
	[string]$EfsPath = '',
	[string]$EfsPlain = '',
	[ValidatePattern('^(|[0-9a-f]{64})$')][string]$EfsSha256 = '',
	[ValidatePattern('^(|[A-Za-z0-9._-]{1,64})$')][string]$Baseline = '',
	[string]$BaselineReceipt = '',
	[string]$TestBinary = '',
	[string]$Test2Json = '',
	[string]$Linger = '',
	[ValidateRange(60, 7200)][int]$TimeoutSeconds = 1200,
	[switch]$ListPrerequisites,
	[switch]$DisposableLab
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

#region pure functions: parameters in, values out, no machine state
# The case table: each case's variants, account and mode come from the
# matrix; here are what the driver needs to run each and how it observes.
function Get-HeadlessCase([string]$Case, [string]$Variant, [string]$Control = '') {
	$account = switch -Regex ($Variant) {
		'^(A|s4u-A|linger-A|stop-A|revocation|shutdown|deadline|security-A|legacy-repair)$' { 'A'; break }
		'^(B|s4u-B|wts-B|linger-B|stop-B|logoff-B|admission-B|security-B|go-tests|standard-wts|peer-denial)$' { 'B'; break }
		'^filtered-admin$' { 'admin'; break }
		default { 'system' }
	}
	$mode = switch -Regex ("$Case/$Variant") {
		'^(G1/|G3/wts-B|G4/(logoff|admission)-B|G6/(go-tests|standard-wts|peer-denial))' { 'wts'; break }
		'^G6/filtered-admin' { 'filtered-admin'; break }
		'^(G6/system-protection|H21/sensitivity|H22/)' { 'system'; break }
		default { 's4u' }
	}
	$needs = @('fixture', 'admission', 'case-root', 'results', 'accounts')
	$needs += switch -Regex ("$Case/$Variant#$Control") {
		'^G1/' { @('wts-client'); break }
		'^G3/wts-B' { @('wts-client'); break }
		'^G4/(logoff|admission)-B' { @('wts-client'); break }
		'^G6/go-tests' { @('wts-client', 'session-runner', 'test-binary', 'test2json'); break }
		'^G6/(standard-wts|peer-denial)' { @('wts-client', 'session-runner'); break }
		'^G6/filtered-admin' { @('wts-client', 'admin-account'); break }
		'^H0[12]/' { @('baseline', 'admin-account'); break }
		'^H0[46]/' { @('wts-client'); break }
		'^H17/[AB]#$' { @('peer-echo'); break }
		'#peer-receipt$' { @('peer-receipt'); break }
		'^H18/[AB]#$' { @('smb-share'); break }
		'#password-share$' { @('smb-share', 'password-runner'); break }
		'#server-principal$' { @('peer-audit'); break }
		'^H19/B#$' { @('efs-fixture'); break }
		'#password-decrypt$' { @('efs-fixture', 'password-runner'); break }
		'^H2[01]/' { @('test-binary', 'test2json'); break }
		'^H22/' { @('baseline', 'admin-account'); break }
		default { @() }
	}
	$stages = switch ($Case) { { $_ -in @('H01', 'H02') } { @('baseline', 'prepare', 'collect'); break } 'H03' { @('prepare', 'collect'); break } default { @('run') } }
	return [ordered]@{ Case = $Case; Variant = $Variant; Account = $account; Mode = $mode; Needs = @($needs); Stages = @($stages) }
}

# Which prerequisites the given parameter values leave missing.
function Get-MissingPrerequisites([string[]]$Needs, [hashtable]$Provided) {
	$fields = @{
		'fixture' = @('Fixture'); 'admission' = @('Admission'); 'case-root' = @('CaseRoot'); 'results' = @('Results')
		'accounts' = @('SidA', 'SidB', 'AccountA', 'AccountB', 'BaseA', 'BaseB'); 'admin-account' = @('AdminSid', 'AdminAccount', 'AdminBase')
		'wts-client' = @('WtsClient'); 'password-runner' = @('PasswordRunner'); 'session-runner' = @('SessionRunner')
		'peer-echo' = @('PeerEcho'); 'peer-receipt' = @('PeerReceipt'); 'peer-audit' = @('PeerAudit')
		'smb-share' = @('SmbServer', 'SmbPath', 'SmbSha256'); 'efs-fixture' = @('EfsPath', 'EfsPlain', 'EfsSha256')
		'baseline' = @('Baseline', 'BaselineReceipt'); 'test-binary' = @('TestBinary'); 'test2json' = @('Test2Json')
	}
	$missing = @()
	foreach ($need in $Needs) {
		foreach ($field in $fields[$need]) {
			if (!$Provided.ContainsKey($field) -or [string]::IsNullOrEmpty([string]$Provided[$field])) { $missing += "$need ($field)" }
		}
	}
	return , @($missing)
}

# The record key of the matrix: CASE/VARIANT[/REPETITION][#CONTROL].
function Get-HeadlessKey([string]$Case, [string]$Variant, [string]$Repetition, [string]$Control) {
	$key = "$Case/$Variant"
	if ($Repetition) { $key += "/$Repetition" }
	if ($Control) { $key += "#$Control" }
	return $key
}

# The observer's plan for a case: what it crashes, how long each generation
# may live, what it releases and how long it watches.
function Get-ObserverPlan([string]$Case, [string]$Variant, [string]$Target, [string]$Class = 's4u', [string]$ReleaseFile = '') {
	$five = @(1..30 | ForEach-Object { '5s' }) -join ','
	switch -Regex ("$Case/$Variant") {
		'^G[12]/' { return @('--target', $Target, '--crash', 'manager', '--crash-class', $Class, '--crash-lives', (@(1..18 | ForEach-Object { '5s' }) -join ','), '--duration', '1500s') }
		'^G3/' { return @('--target', $Target, '--crash', 'manager', '--crash-class', $Class, '--crash-lives', ((@(1..6 | ForEach-Object { '5s' }) + @('130s') + @(1..3 | ForEach-Object { '5s' })) -join ','), '--duration', '1200s') }
		'^G4/' { return @('--target', $Target, '--crash', 'manager', '--crash-class', $Class, '--crash-lives', $five, '--duration', '1500s') }
		'^G5/' { return @('--target', $Target, '--release-file', $ReleaseFile, '--release-after', '410', '--duration', '900s') }
		'^H07/' { return @('--target', $Target, '--crash', 'workload', '--crash-lives', '10s', '--duration', '120s') }
		'^H08/' { return @('--target', $Target, '--crash', 'manager', '--crash-class', 's4u', '--crash-lives', '10s', '--duration', '180s') }
		'^H09/' { return @('--crash', 'broker', '--crash-lives', '10s', '--duration', '300s') }
		'^H0[12]/' { return @('--duration', '900s') }
		'^H03/' { return @('--duration', '180s') }
		default { return @('--duration', '600s') }
	}
}

# One record's observation, as `headless-workload record` reads it. The
# observer report, when there is one, is passed separately.
function New-HeadlessObservation {
	param([string]$Key, [string]$Kind, [string]$Result, $Token, [string]$ExecutionId, [int]$Sequence, [string]$BootId,
		[int]$PasswordLogons, [string]$RunnerId, [bool]$Cleanup, [string[]]$Controls, $Evidence, [string]$Detail)
	$o = [ordered]@{ key = $Key; kind = $Kind; result = $Result }
	if ($null -ne $Token) { $o.token = $Token }
	$o.executionId = if ($Kind -eq 'control' -and !$ExecutionId) { '' } else { $ExecutionId }
	$o.sequence = $Sequence
	$o.bootId = $BootId
	$o.passwordLogons = $PasswordLogons
	if ($RunnerId) { $o.runnerId = $RunnerId }
	$o.cleanupConfirmed = $Cleanup
	if ($Controls -and $Controls.Count -gt 0) { $o.controls = @($Controls) }
	$o.evidence = if ($null -ne $Evidence) { $Evidence } else { [ordered]@{} }
	if ($Detail) { $o.detail = $Detail }
	return $o
}

# The token context a record claims; the summary checks it against the
# probes and the observer.
function Get-HeadlessToken([string]$Mode, [string]$Sid, [int]$Session) {
	switch ($Mode) {
		's4u' { return [ordered]@{ sid = $Sid; session = 0; elevated = $false; source = 's4u' } }
		'wts' { return [ordered]@{ sid = $Sid; session = $Session; elevated = $false; source = 'wts' } }
		'filtered-admin' { return [ordered]@{ sid = $Sid; session = $Session; elevated = $false; source = 'process' } }
		'password' { return [ordered]@{ sid = $Sid; session = $Session; elevated = $false; source = 'password' } }
		'system' { return [ordered]@{ sid = 'S-1-5-18'; session = 0; elevated = $true; source = 'process' } }
		'peer' { return [ordered]@{ session = 0; elevated = $false; source = 'peer' } }
	}
	throw "Unknown mode $Mode"
}

# Windows command-line quoting for one argument (CommandLineToArgvW rules).
function ConvertTo-NativeArgument([string]$Value) {
	if ($Value -and $Value -notmatch '[\s"]') { return $Value }
	$escaped = [regex]::Replace($Value, '(\\*)"', '$1$1\"')
	return '"' + [regex]::Replace($escaped, '(\\+)$', '$1$1') + '"'
}

# Record details: machine values replaced by placeholders, longest first.
function Protect-Detail([string]$Text, [object[]]$Pairs) {
	foreach ($pair in ($Pairs | Where-Object { $_[0] } | Sort-Object { $_[0].Length } -Descending)) { $Text = $Text.Replace($pair[0], $pair[1]) }
	return $Text -replace '[\r\n]+', ' '
}
#endregion

$caseInfo = Get-HeadlessCase -Case $Case -Variant $Variant -Control $Control
$provided = @{}
foreach ($name in $PSBoundParameters.Keys) { $provided[$name] = $PSBoundParameters[$name] }
$missing = Get-MissingPrerequisites -Needs $caseInfo.Needs -Provided $provided
if ($ListPrerequisites) {
	[ordered]@{ case = $Case; variant = $Variant; account = $caseInfo.Account; mode = $caseInfo.Mode; stages = $caseInfo.Stages
		needs = $caseInfo.Needs; missing = $missing } | ConvertTo-Json -Depth 4
	exit 0
}
if (!$DisposableLab) { throw 'Explicit disposable-lab acknowledgement required' }
if (!$InstallDir) { $InstallDir = Join-Path $env:ProgramFiles 'winunitd\bin' }
if (!$DataDir) { $DataDir = Join-Path $env:ProgramData 'winunitd' }
if (!$Linger) { $Linger = Join-Path $DataDir 'linger' }
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'Expected SYSTEM' }
if ($missing.Count -gt 0) { throw "Missing prerequisites: $($missing -join ', ')" }
if ($Stage -notin $caseInfo.Stages) { throw "Stage $Stage does not apply to $Case" }
foreach ($path in @(@($Fixture, $Admission, $CaseRoot, $Results, $InstallDir, $DataDir) + @($BaselineReceipt | Where-Object { $_ }))) {
	if (![IO.Path]::IsPathRooted($path) -or $path -match '"') { throw 'Every path parameter must be absolute' }
}

$daemon = Join-Path $InstallDir 'winunitd.exe'
$winctl = Join-Path $InstallDir 'winctl.exe'
$key = Get-HeadlessKey -Case $Case -Variant $Variant -Repetition $Repetition -Control $Control
$caseDir = Join-Path $CaseRoot (($key -replace '[/#]', '-').ToLowerInvariant() + "-$Stage")
$sidOf = @{ A = $SidA; B = $SidB; admin = $AdminSid; system = 'S-1-5-18' }
$accountOf = @{ A = $AccountA; B = $AccountB; admin = $AdminAccount }
$baseOf = @{ A = $BaseA; B = $BaseB; admin = $AdminBase; system = $DataDir }
$role = $caseInfo.Account
$sid = $sidOf[$role]
$peerRole = if ($role -eq 'A') { 'B' } else { 'A' }

# The admitted build, checked before anything runs.
$manifest = Get-Content -LiteralPath $Admission -Raw | ConvertFrom-Json
if ($manifest.schema -ne 1 -or $manifest.source -notmatch '^[0-9a-f]{40}$' -or $manifest.dirty) { throw 'The admission manifest must name one clean, full source commit' }
function Get-AdmittedHash([string]$Name) {
	$found = @($manifest.artifacts | Where-Object { $_.name -eq $Name })
	if ($found.Count -ne 1 -or $found[0].sha256 -notmatch '^[0-9a-f]{64}$') { throw "The admission manifest does not admit $Name" }
	return $found[0].sha256
}
$admitted = @{ $Fixture = Get-AdmittedHash 'headless-workload.exe'; $daemon = Get-AdmittedHash 'winunitd.exe'; $winctl = Get-AdmittedHash 'winctl.exe' }
if ($TestBinary) { $admitted[$TestBinary] = Get-AdmittedHash (Split-Path -Leaf $TestBinary) }
if ($Test2Json) { $admitted[$Test2Json] = Get-AdmittedHash 'test2json.exe' }
foreach ($file in $admitted.Keys) {
	if ((Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant() -ne $admitted[$file]) { throw "$(Split-Path -Leaf $file) is not the admitted build" }
}

$pairs = @(@($caseDir, '<case>'), @($CaseRoot, '<case-root>'), @($Results, '<results>'), @($InstallDir, '<install>'), @($DataDir, '<data>'),
	@($Fixture, '<fixture>'), @($Admission, '<admission>'), @($BaseA, '<base-a>'), @($BaseB, '<base-b>'), @($AdminBase, '<base-admin>'),
	@($SidA, '<sid-a>'), @($SidB, '<sid-b>'), @($AdminSid, '<sid-admin>'), @($AccountA, '<account-a>'), @($AccountB, '<account-b>'),
	@($AdminAccount, '<account-admin>'), @($SmbServer, '<smb-server>'), @($SmbPath, '<smb-path>'), @($PeerEcho, '<peer>'), @($EfsPath, '<efs>'))

function Stop-ExactProcess([Diagnostics.Process]$Process, [string]$What) {
	try { if (!$Process.HasExited) { $Process.Kill() } } catch { }
	if ($Process.WaitForExit(30000)) { return "$What was killed after its deadline" }
	return "$What did not exit after kill: cleanup unconfirmed, case evidence kept"
}

function Invoke-Native {
	param([string]$File, [string[]]$Arguments, [int[]]$Expected = @(0), [int]$TimeoutMs = 300000)
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

function Wait-Until([scriptblock]$Condition, [string]$What, [int]$Seconds = 300) {
	$deadline = (Get-Date).AddSeconds($Seconds)
	while (!(& $Condition)) {
		if ((Get-Date) -ge $deadline) { throw "Timed out waiting for $What" }
		Start-Sleep -Milliseconds 250
	}
}

function Get-BootId { return (Invoke-Native -File $Fixture -Arguments @('boot-id')).Trim() }

function Get-UserManagerPids([string]$ManagerSid) {
	return @(Get-CimInstance Win32_Process -Filter "Name='winunitd.exe'" | Where-Object { $_.CommandLine -match "--user-manager\s+$([regex]::Escape($ManagerSid))(\s|$)" } |
		ForEach-Object { [uint32]$_.ProcessId })
}

# The observer, started as a child of this SYSTEM driver.
$script:observer = $null
function Start-Observer([string[]]$Plan, [string]$Report, [string]$Marks = '', [string]$StopFile = '', [string[]]$Accounts = @('A', 'B'),
	[string]$WorkloadImage = $Fixture) {
	$arguments = @('observe', '--report', $Report, '--admission', $Admission, '--daemon-image', $daemon, '--workload-image', $WorkloadImage)
	foreach ($a in $Accounts) { if ($sidOf[$a]) { $arguments += @('--account', "$a=$($sidOf[$a])") } }
	if ($Marks) { $arguments += @('--marks', $Marks) }
	if ($StopFile) { $arguments += @('--stop-file', $StopFile) }
	$arguments += $Plan
	$start = New-Object Diagnostics.ProcessStartInfo
	$start.FileName = $Fixture
	$start.Arguments = ($arguments | ForEach-Object { ConvertTo-NativeArgument $_ }) -join ' '
	$start.UseShellExecute = $false
	$start.CreateNoWindow = $true
	$script:observer = [Diagnostics.Process]::Start($start)
	Wait-Until { Test-Path -LiteralPath $Report } 'the observer report' 60
}

function Wait-Observer([string]$Report, [int]$Seconds) {
	if ($null -eq $script:observer) { return $null }
	if (!$script:observer.WaitForExit($Seconds * 1000)) { throw "The observer did not finish: $(Stop-ExactProcess $script:observer 'the observer')" }
	$rep = Get-Content -LiteralPath $Report -Raw | ConvertFrom-Json
	if ($rep.stage -ne 'finished') { throw 'The observer did not finish its observation' }
	return $rep
}

function Read-ObserverReport([string]$Report) {
	try { return Get-Content -LiteralPath $Report -Raw | ConvertFrom-Json } catch { return $null }
}

function New-Mark([string]$Marks, [string]$Name) { New-Item -ItemType File -Path (Join-Path $Marks $Name) | Out-Null }

# Linger and the broker, through the product.
function Set-Linger([string]$Role, [bool]$Enabled) {
	$verb = if ($Enabled) { 'enable-linger' } else { 'disable-linger' }
	Invoke-Native -File $winctl -Arguments @($verb, $accountOf[$Role]) | Out-Null
}
function Stop-Broker {
	Stop-Service -Name winunitd
	Wait-Until { (Get-Service winunitd).Status -eq 'Stopped' } 'the broker to stop' 180
	Wait-Until { @(@($SidA, $SidB, $AdminSid) | Where-Object { $_ } | ForEach-Object { Get-UserManagerPids $_ }).Count -eq 0 } 'the user managers to exit' 180
}
function Start-Broker {
	Start-Service -Name winunitd
	Wait-Until { (Get-Service winunitd).Status -eq 'Running' } 'the broker to start' 180
}

# Interactive admission, by the protected machine policy file.
function Set-InteractiveAdmission([string]$Sid, [string]$State) {
	$path = Join-Path $DataDir 'user-admission.json'
	$policy = if (Test-Path -LiteralPath $path) { Get-Content -LiteralPath $path -Raw | ConvertFrom-Json } else { [pscustomobject]@{ mode = 'explicit'; users = [pscustomobject]@{} } }
	$users = [ordered]@{}
	foreach ($p in $policy.users.PSObject.Properties) { $users[$p.Name] = $p.Value }
	$users[$Sid] = $State
	$tmp = "$path.tmp"
	[ordered]@{ mode = $policy.mode; users = $users } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $tmp -Encoding UTF8
	& icacls.exe $tmp /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' '*S-1-5-32-545:R' | Out-Null
	Move-Item -LiteralPath $tmp -Destination $path -Force
	Invoke-Native -File $winctl -Arguments @('daemon-reload') | Out-Null
}

# The baseline: SYSTEM's receipt of the machine state before any case, and
# the interactive admission policy as it was, kept beside the receipt so
# the final cleanup can put it back exactly. Both are written once.
$inventoryScope = @('--account', "A=$SidA", '--account', "B=$SidB", '--account', "admin=$AdminSid", '--data-dir', $DataDir, '--linger', $Linger)
$admissionPolicy = Join-Path $DataDir 'user-admission.json'
function New-BaselineReceipt {
	if (Test-Path -LiteralPath "$BaselineReceipt.admission") { throw 'The baseline was already taken' }
	Invoke-Native -File $Fixture -Arguments (@('inventory', '--receipt', $Baseline) + $inventoryScope + @('--out', $BaselineReceipt)) | Out-Null
	if (Test-Path -LiteralPath $admissionPolicy) {
		Copy-Item -LiteralPath $admissionPolicy -Destination "$BaselineReceipt.admission"
		(Get-Acl -LiteralPath $admissionPolicy).Sddl | Set-Content -LiteralPath "$BaselineReceipt.admission.sddl" -Encoding ASCII
	} else {
		New-Item -ItemType File -Path "$BaselineReceipt.admission.absent" | Out-Null
	}
}
function Restore-AdmissionBaseline {
	if (Test-Path -LiteralPath "$BaselineReceipt.admission") {
		$tmp = "$admissionPolicy.tmp"
		Copy-Item -LiteralPath "$BaselineReceipt.admission" -Destination $tmp -Force
		$acl = Get-Acl -LiteralPath $tmp
		$acl.SetSecurityDescriptorSddlForm((Get-Content -LiteralPath "$BaselineReceipt.admission.sddl" -Raw).Trim())
		Set-Acl -LiteralPath $tmp -AclObject $acl
		Move-Item -LiteralPath $tmp -Destination $admissionPolicy -Force
	} elseif (Test-Path -LiteralPath "$BaselineReceipt.admission.absent") {
		if (Test-Path -LiteralPath $admissionPolicy) { Remove-Item -LiteralPath $admissionPolicy -Force }
	} else {
		throw 'No saved admission policy beside the baseline receipt'
	}
	Invoke-Native -File $winctl -Arguments @('daemon-reload') | Out-Null
}

# Lab commands.
function Invoke-Wts([string]$Action, [string]$Role) { Invoke-Native -File $WtsClient -Arguments @($Action, $accountOf[$Role]) | Out-Null }
function Invoke-AsPassword([string]$Role, [string[]]$Arguments) { Invoke-Native -File $PasswordRunner -Arguments (@($accountOf[$Role], $Fixture) + $Arguments) | Out-Null }
function Invoke-InSession([string]$Role, [string]$Exe, [string[]]$Arguments) {
	Invoke-Native -File $SessionRunner -Arguments (@($accountOf[$Role], $Exe) + $Arguments) | Out-Null
}

# A command on the workload's pipe, as SYSTEM: health, or a named probe the
# workload runs inside its unit. Returns the reply.
function Invoke-Workload([string]$WorkloadSid, [hashtable]$Command) {
	$pipe = New-Object IO.Pipes.NamedPipeClientStream('.', "winunitd-qual\workload\$WorkloadSid", [IO.Pipes.PipeDirection]::InOut)
	try {
		$pipe.Connect(30000)
		$writer = New-Object IO.StreamWriter($pipe)
		$writer.AutoFlush = $true
		$writer.WriteLine(($Command | ConvertTo-Json -Compress))
		$reader = New-Object IO.StreamReader($pipe)
		$line = $reader.ReadLine()
		$reply = $line | ConvertFrom-Json
		if (!$reply.ok) { throw "workload command $($Command.verb) failed" }
		return $reply
	} finally { $pipe.Dispose() }
}
function Get-Probe([string]$WorkloadSid, [string]$Name) { return (Invoke-Workload $WorkloadSid @{ verb = 'probe'; probe = $Name }).probe }

# The workload's probe configuration, in its own state root.
function Set-ProbeConfig([string]$Role, [hashtable]$Config) {
	$state = Join-Path $baseOf[$Role] '..\winunitd-qual\headless-workload'
	$state = [IO.Path]::GetFullPath($state)
	New-Item -ItemType Directory -Path $state -Force | Out-Null
	$Config | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $state 'config.json') -Encoding UTF8
}

$records = New-Object Collections.Generic.List[object]
$failures = New-Object Collections.Generic.List[string]
$checks = New-Object Collections.Generic.List[string]
$cleanup = $true
$caseCreated = $false
$transcript = $false
$lingerChanged = @()
$brokerStopped = $false
$sessionOpen = @()
$report = Join-Path $caseDir 'observer.json'
$marks = Join-Path $caseDir 'marks'
$stop = Join-Path $caseDir 'observer-stop'
$observerReport = $null

function Add-Record([string]$RecordKey, [string]$Kind, $Token, $Evidence, [string[]]$Controls = @(), [string]$Observer = '', [string]$Runner = '') {
	$records.Add([ordered]@{ Key = $RecordKey; Kind = $Kind; Token = $Token; Evidence = $Evidence; Controls = $Controls; Observer = $Observer; Runner = $Runner })
}

try {
	if (Test-Path -LiteralPath $caseDir) { throw 'Fresh case directory required' }
	New-Item -ItemType Directory -Path $caseDir | Out-Null
	$caseCreated = $true
	# SYSTEM, Administrators and the qualification accounts, which write
	# probe outputs here through the lab commands.
	$grants = @('*S-1-5-18:(OI)(CI)F', '*S-1-5-32-544:(OI)(CI)F')
	foreach ($s in @($SidA, $SidB, $AdminSid)) { if ($s) { $grants += "*${s}:(OI)(CI)M" } }
	& icacls.exe $caseDir /inheritance:r /grant:r @grants | Out-Null
	New-Item -ItemType Directory -Path $marks | Out-Null
	Start-Transcript -LiteralPath (Join-Path $caseDir 'driver.log') | Out-Null
	$transcript = $true
	$session = 0

	switch -Regex ("$Case/$Variant/$Stage/$Control") {
		# Cold boot: SYSTEM's first-use check and a startup observer before
		# the controller reboots; the records after it.
		'^H0[12]/[AB]/baseline/' {
			New-BaselineReceipt
			$checks.Add('baseline receipt taken; the lab provisions next')
			break
		}
		'^H0[12]/[AB]/prepare/' {
			$firstUse = Join-Path $caseDir 'first-use.json'
			if (!(Test-Path -LiteralPath $BaselineReceipt)) { throw 'No baseline receipt: run the baseline stage on the sealed baseline before provisioning' }
			Invoke-Native -File $Fixture -Arguments @('probe-first-use', '--sid', $SidA, '--baseline-receipt', $BaselineReceipt, '--out', $firstUse) | Out-Null
			Add-Record (Get-HeadlessKey 'H01' 'A' '' 'first-use') 'control' (Get-HeadlessToken 'system' '' 0) ([ordered]@{ firstUse = (Get-Content -LiteralPath $firstUse -Raw | ConvertFrom-Json) })
			$bootReport = Join-Path $CaseRoot 'cold-boot-observer.json'
			$taskArgs = (@('observe', '--report', $bootReport, '--admission', $Admission, '--daemon-image', $daemon, '--workload-image', $Fixture,
					'--account', "A=$SidA", '--account', "B=$SidB") + (Get-ObserverPlan 'H01' 'A' 'A') | ForEach-Object { ConvertTo-NativeArgument $_ }) -join ' '
			$action = New-ScheduledTaskAction -Execute $Fixture -Argument $taskArgs
			$trigger = New-ScheduledTaskTrigger -AtStartup
			Register-ScheduledTask -TaskPath '\winunitd-qual\' -TaskName 'cold-boot-observer' -Action $action -Trigger $trigger -User 'SYSTEM' -RunLevel Highest -Force | Out-Null
			$checks.Add('first-use check recorded; startup observer staged; the controller reboots next')
			break
		}
		'^H0[12]/[AB]/collect/' {
			$bootReport = Join-Path $CaseRoot 'cold-boot-observer.json'
			Wait-Until { (Read-ObserverReport $bootReport).stage -eq 'finished' } 'the cold-boot observer' $TimeoutSeconds
			Unregister-ScheduledTask -TaskPath '\winunitd-qual\' -TaskName 'cold-boot-observer' -Confirm:$false
			Add-Record (Get-HeadlessKey 'H01' 'A' '' '') 'primary' (Get-HeadlessToken 's4u' $SidA 0) $null @('H01/A#first-use') $bootReport
			Add-Record (Get-HeadlessKey 'H02' 'B' '' '') 'primary' (Get-HeadlessToken 's4u' $SidB 0) $null @() $bootReport
			break
		}
		# No implicit grant: grants removed, a startup observer, the reboot.
		'^H03/[AB]/prepare/' {
			Set-Linger 'A' $false; Set-Linger 'B' $false
			$lingerChanged = @('A', 'B')
			$bootReport = Join-Path $CaseRoot 'no-grant-observer.json'
			$taskArgs = (@('observe', '--report', $bootReport, '--admission', $Admission, '--daemon-image', $daemon, '--workload-image', $Fixture,
					'--account', "A=$SidA", '--account', "B=$SidB") + (Get-ObserverPlan 'H03' 'A' 'A') | ForEach-Object { ConvertTo-NativeArgument $_ }) -join ' '
			$action = New-ScheduledTaskAction -Execute $Fixture -Argument $taskArgs
			Register-ScheduledTask -TaskPath '\winunitd-qual\' -TaskName 'no-grant-observer' -Action $action -Trigger (New-ScheduledTaskTrigger -AtStartup) -User 'SYSTEM' -RunLevel Highest -Force | Out-Null
			$lingerChanged = @()
			$checks.Add('grants removed; startup observer staged; the controller reboots next')
			break
		}
		'^H03/[AB]/collect/' {
			$bootReport = Join-Path $CaseRoot 'no-grant-observer.json'
			Wait-Until { (Read-ObserverReport $bootReport).stage -eq 'finished' } 'the no-grant observer' $TimeoutSeconds
			Unregister-ScheduledTask -TaskPath '\winunitd-qual\' -TaskName 'no-grant-observer' -Confirm:$false
			Add-Record (Get-HeadlessKey 'H03' 'A' '' '') 'primary' (Get-HeadlessToken 's4u' $SidA 0) $null @() $bootReport
			Add-Record (Get-HeadlessKey 'H03' 'B' '' '') 'primary' (Get-HeadlessToken 's4u' $SidB 0) $null @() $bootReport
			Set-Linger 'A' $true; Set-Linger 'B' $true
			break
		}
		# Crashes the observer injects: H07 unit, H08 manager, H09 broker.
		'^H0[789]/' {
			Start-Observer (Get-ObserverPlan $Case $Variant $role) $report
			$observerReport = Wait-Observer $report 600
			if ($Case -eq 'H09') {
				Add-Record 'H09/A' 'primary' (Get-HeadlessToken 's4u' $SidA 0) $null @() $report
				Add-Record 'H09/B' 'primary' (Get-HeadlessToken 's4u' $SidB 0) $null @() $report
			} else {
				Add-Record $key 'primary' (Get-HeadlessToken 's4u' $sid 0) $null @() $report
			}
			break
		}
		# Crash loops and the stable reset: the observer crashes each new
		# manager on its plan; WTS rows hold B's live session meanwhile.
		'^G[123]/' {
			$class = 's4u'
			if ($caseInfo.Mode -eq 'wts') {
				# Only the session manager runs for the account meanwhile.
				Set-Linger $role $false; $lingerChanged += $role
				Invoke-Wts 'logon' $role; $sessionOpen += $role
				$class = 'wts'
			}
			Start-Observer (Get-ObserverPlan $Case $Variant $role $class) $report
			$observerReport = Wait-Observer $report 1600
			if ($caseInfo.Mode -eq 'wts') {
				$session = [int]@($observerReport.sessions | ForEach-Object { $_.users } | Where-Object { $_.sid -eq $sid } | Select-Object -First 1).session
			}
			Add-Record $key 'primary' (Get-HeadlessToken $caseInfo.Mode $sid $session) $null @() $report
			break
		}
		# Cancellation of a waiting recovery: a crash loop until the delay is
		# capped, then the intervention right after a crash, while its retry
		# waits, with the quiet mark first.
		'^G4/' {
			$class = 's4u'
			if ($caseInfo.Mode -eq 'wts') {
				Set-Linger $role $false; $lingerChanged += $role
				Invoke-Wts 'logon' $role; $sessionOpen += $role
				$class = 'wts'
			}
			Start-Observer (Get-ObserverPlan $Case $Variant $role $class) $report $marks $stop
			Wait-Until {
				$r = Read-ObserverReport $report
				if ($null -eq $r) { return $false }
				$crashed = @($r.generations | Where-Object { $_.role -eq 'manager' -and $_.account -eq $role -and $_.crashed })
				if ($crashed.Count -lt 9) { return $false }
				$last = ($crashed | Sort-Object { [uint64]$_.crashed } | Select-Object -Last 1)
				$now = [DateTime]::UtcNow.ToFileTimeUtc()
				return ($now - [int64]$last.crashed) -lt 100000000 # within 10 s of the last crash
			} 'a capped, waiting recovery' 1200
			New-Mark $marks 'quiet'
			switch -Regex ($Variant) {
				'^linger-' { Set-Linger $role $false; if ($role -notin $lingerChanged) { $lingerChanged += $role } }
				'^stop-' {
					Stop-Broker; $brokerStopped = $true
					Remove-Item -LiteralPath (Join-Path $Linger $sid) -Force
					$lingerChanged += $role
					Start-Broker; $brokerStopped = $false
				}
				'^logoff-' { Invoke-Wts 'logoff' $role; $sessionOpen = @($sessionOpen | Where-Object { $_ -ne $role }) }
				'^admission-' { Set-InteractiveAdmission $sid 'disabled' }
			}
			Start-Sleep -Seconds 140
			New-Item -ItemType File -Path $stop | Out-Null
			$observerReport = Wait-Observer $report 120
			if ($Variant -like 'admission-*') { Set-InteractiveAdmission $sid 'enabled' }
			Add-Record $key 'primary' (Get-HeadlessToken $caseInfo.Mode $sid $session) $null @() $report
			break
		}
		# Unlimited unit recovery: the failing unit runs from a byte-equal
		# copy of the fixture so the observer watches only it; the workload
		# reads the unit's status from its own manager.
		'^G5/[AB]/run/$' {
			# The failing unit runs a byte-equal copy of the fixture, provisioned
			# as `fail --code 7 --until <its directory>\release-<role> --hold 500ms`.
			$failImage = Join-Path (Split-Path -Parent $Fixture) 'headless-fail\headless-workload.exe'
			if (!(Test-Path -LiteralPath $failImage)) { throw 'The failing unit image must be provisioned beside the fixture' }
			$release = Join-Path (Split-Path -Parent $failImage) "release-$role"
			if (Test-Path -LiteralPath $release) { Remove-Item -LiteralPath $release }
			Set-ProbeConfig $role @{ winctl = $winctl; statusUnit = 'failing.service' }
			Start-Observer (Get-ObserverPlan $Case $Variant $role 's4u' $release) $report '' '' @('A', 'B') $failImage
			# A fresh manager starts the enabled failing unit under observation.
			Set-Linger $role $false; Set-Linger $role $true
			$observerReport = Wait-Observer $report 1000
			$status = Get-Probe $sid 'unit-status'
			Add-Record $key 'primary' (Get-HeadlessToken 's4u' $sid 0) ([ordered]@{ status = $status }) @("$key#finite-limit") $report
			break
		}
		'^G5/[AB]/run/finite-limit$' {
			$finiteImage = Join-Path (Split-Path -Parent $Fixture) 'headless-finite\headless-workload.exe'
			Set-ProbeConfig $role @{ winctl = $winctl; statusUnit = 'finite.service' }
			if (!(Test-Path -LiteralPath $finiteImage)) { throw 'The finite unit image must be provisioned beside the fixture' }
			Start-Observer @('--duration', '120s') $report '' '' @('A', 'B') $finiteImage
			Set-Linger $role $false; Set-Linger $role $true
			$observerReport = Wait-Observer $report 200
			$status = Get-Probe $sid 'unit-status'
			Add-Record $key 'control' (Get-HeadlessToken 's4u' $sid 0) ([ordered]@{ status = $status }) @() $report
			break
		}
		# Same-account WTS logon and logoff beside the headless manager.
		'^H04/' {
			Start-Observer (Get-ObserverPlan $Case $Variant $role) $report $marks $stop
			Start-Sleep -Seconds 10
			Invoke-Wts 'logon' $role; $sessionOpen += $role
			Start-Sleep -Seconds 60
			Invoke-Wts 'logoff' $role; $sessionOpen = @($sessionOpen | Where-Object { $_ -ne $role })
			Start-Sleep -Seconds 20
			New-Item -ItemType File -Path $stop | Out-Null
			$observerReport = Wait-Observer $report 120
			Add-Record $key 'primary' (Get-HeadlessToken 's4u' $sid 0) $null @() $report
			break
		}
		# Revocation with no session: the quiet mark, linger disabled, drain.
		'^H05/' {
			Start-Observer (Get-ObserverPlan $Case $Variant $role) $report $marks $stop
			Start-Sleep -Seconds 10
			New-Mark $marks 'quiet'
			Set-Linger $role $false; $lingerChanged += $role
			Start-Sleep -Seconds 90
			New-Item -ItemType File -Path $stop | Out-Null
			$observerReport = Wait-Observer $report 120
			Add-Record $key 'primary' (Get-HeadlessToken 's4u' $sid 0) $null @() $report
			break
		}
		# Independent permissions, marked in order.
		'^H06/' {
			Start-Observer (Get-ObserverPlan $Case $Variant $role) $report $marks $stop
			Start-Sleep -Seconds 10
			New-Mark $marks 'admission-revoked'; Set-InteractiveAdmission $sid 'disabled'
			Start-Sleep -Seconds 20
			# Restoring admission before the session is its own declared step.
			New-Mark $marks 'admission-restored'; Set-InteractiveAdmission $sid 'enabled'
			Start-Sleep -Seconds 5
			Invoke-Wts 'logon' $role; $sessionOpen += $role
			Start-Sleep -Seconds 20
			New-Mark $marks 'linger-disabled'; Set-Linger $role $false; $lingerChanged += $role
			Start-Sleep -Seconds 30
			New-Mark $marks 'logoff'; Invoke-Wts 'logoff' $role; $sessionOpen = @($sessionOpen | Where-Object { $_ -ne $role })
			Start-Sleep -Seconds 90
			New-Item -ItemType File -Path $stop | Out-Null
			$observerReport = Wait-Observer $report 120
			Add-Record $key 'primary' (Get-HeadlessToken 's4u' $sid 0) $null @() $report
			break
		}
		# Rotation of a padded stopped log, and repair or protection.
		'^(H12/|G6/(standard-wts|filtered-admin|legacy-repair))' {
			$root = $baseOf[$role]
			$before = Join-Path $caseDir 'daemon-log-before.json'
			$after = Join-Path $caseDir 'daemon-log.json'
			if ($caseInfo.Mode -ne 's4u') {
				if ($role -ne 'admin') { Set-Linger $role $false; $lingerChanged += $role }
				Invoke-Wts 'logon' $role; $sessionOpen += $role
			}
			Stop-Broker; $brokerStopped = $true
			if ($Variant -eq 'legacy-repair') {
				# A legacy directory: owned by the account, its access open.
				$dir = Join-Path $root 'daemon'
				New-Item -ItemType Directory -Path $dir -Force | Out-Null
				& icacls.exe $dir /setowner "*$sid" | Out-Null
				& icacls.exe $dir /reset | Out-Null
				& icacls.exe $dir /grant "*${sid}:(OI)(CI)F" '*S-1-5-32-545:(OI)(CI)F' | Out-Null
			} else {
				Invoke-Native -File $Fixture -Arguments @('pad-log', '--path', (Join-Path $root 'daemon\daemon.log')) | Out-Null
			}
			Invoke-Native -File $Fixture -Arguments @('probe-daemon-log', '--root', $root, '--sid', $sid, '--phase', 'before', '--out', $before) | Out-Null
			Start-Observer @('--duration', '120s') $report '' '' @('A', 'B', 'admin')
			Start-Broker; $brokerStopped = $false
			$observerReport = Wait-Observer $report 200
			Invoke-Native -File $Fixture -Arguments @('probe-daemon-log', '--root', $root, '--sid', $sid, '--phase', 'after', '--before', $before, '--out', $after) | Out-Null
			Add-Record $key 'primary' (Get-HeadlessToken $caseInfo.Mode $sid $session) ([ordered]@{ daemonLog = (Get-Content -LiteralPath $after -Raw | ConvertFrom-Json) }) @() $report
			break
		}
		'^G6/system-protection/' {
			$after = Join-Path $caseDir 'daemon-log.json'
			Invoke-Native -File $Fixture -Arguments @('probe-daemon-log', '--root', $DataDir, '--sid', 'S-1-5-18', '--phase', 'after', '--out', $after) | Out-Null
			Add-Record $key 'primary' (Get-HeadlessToken 'system' '' 0) ([ordered]@{ daemonLog = (Get-Content -LiteralPath $after -Raw | ConvertFrom-Json) })
			break
		}
		# Peer denial from B's own session token.
		'^G6/peer-denial/' {
			Invoke-Wts 'logon' $role; $sessionOpen += $role
			$config = Join-Path $caseDir 'denial-config.json'
			@{ ownDaemon = $BaseB; peerDaemon = $BaseA; systemDaemon = $DataDir } | ConvertTo-Json | Set-Content -LiteralPath $config -Encoding UTF8
			$token = Join-Path $caseDir 'token.json'
			$paths = Join-Path $caseDir 'paths.json'
			Invoke-InSession $role $Fixture @('probe-token', '--out', $token)
			Invoke-InSession $role $Fixture @('probe-path', '--set', 'daemon-denial', '--config', $config, '--out', $paths)
			$tp = (Get-Content -LiteralPath $token -Raw | ConvertFrom-Json).identity
			Add-Record $key 'primary' (Get-HeadlessToken 'wts' $sid ([int]$tp.session)) ([ordered]@{ tokenProbe = $tp; paths = (Get-Content -LiteralPath $paths -Raw | ConvertFrom-Json).paths })
			break
		}
		# Named native tests: SYSTEM runners for H20/H21, B's own session for
		# the daemon-log regressions; test2json turns the output into events.
		'^(H2[01]|G6/go-tests)' {
			$tests = if ($Variant -eq 'go-tests') { @('TestDaemonPathSDDL', 'TestDaemonPathOwnerAllowed', 'TestDaemonLogNonAdminIdentity') } else {
				@{ revocation = 'TestLingerRevocationOverlapsNativeHeadlessManagerCreation'; shutdown = 'TestShutdownOverlapsNativeHeadlessManagerCreation'
					deadline = 'TestShutdownDeadlineOverlapsNativeHeadlessManagerCreation'; 'security-A' = 'TestNativeHeadlessUserManagerSecurity'
					'security-B' = 'TestNativeHeadlessUserManagerSecurity'; 'sensitivity-A' = 'TestNativeSecurityProbeDetectsSelectiveInheritance'
					'sensitivity-B' = 'TestNativeSecurityProbeDetectsSelectiveInheritance' }[$Variant] }
			$run = '^(' + (@($tests) -join '|') + ')$'
			$events = Join-Path $caseDir 'test.json'
			$subject = Join-Path $caseDir 'subject.json'
			$receipt = Join-Path $caseDir 'receipt.json'
			$runner = if ($Repetition) { "runner-$Repetition" } else { "runner-$Variant" }
			$receiptArgs = @('test-receipt', '--run', $run, '--test2json', $Test2Json, '--events', $events, '--artifact', (Split-Path -Leaf $TestBinary),
				'--binary', $TestBinary, '--runner', $runner, '--out', $receipt)
			if ($Variant -eq 'go-tests') {
				# The account's own interactive token runs them, so the
				# identity test cannot pass by skipping.
				Invoke-Wts 'logon' $role; $sessionOpen += $role
				Invoke-InSession $role $Fixture $receiptArgs
			} else {
				$env:WINUNITD_QUAL_SUBJECT_OUT = if ($caseInfo.Mode -eq 's4u') { $subject } else { '' }
				$env:WINUNITD_NATIVE_OVERLAP_HEADLESS_SID = $sid
				$env:WINUNITD_NATIVE_OVERLAP_FIXTURE = 'disposable'
				$env:WINUNITD_NATIVE_SECURITY_OTHER_SID = $sidOf[$peerRole]
				try {
					if ($caseInfo.Mode -eq 's4u') { $receiptArgs += @('--subject', $subject) }
					Invoke-Native -File $Fixture -Arguments $receiptArgs -TimeoutMs 1800000 | Out-Null
				} finally {
					foreach ($name in @('WINUNITD_QUAL_SUBJECT_OUT', 'WINUNITD_NATIVE_OVERLAP_HEADLESS_SID', 'WINUNITD_NATIVE_OVERLAP_FIXTURE', 'WINUNITD_NATIVE_SECURITY_OTHER_SID')) {
						Remove-Item "Env:$name" -ErrorAction SilentlyContinue
					}
				}
			}
			$token = if ($caseInfo.Mode -eq 's4u') { Get-HeadlessToken 's4u' $sid 0 } else { Get-HeadlessToken $caseInfo.Mode $sid $session }
			$records.Add([ordered]@{ Key = $key; Kind = 'primary'; Token = $token; Evidence = [ordered]@{ testRun = (Get-Content -LiteralPath $receipt -Raw | ConvertFrom-Json) }
					Controls = @(); Observer = ''; Runner = $runner })
			break
		}
		# Probes from the workload in its unit, with the observer holding it.
		'^H1[3-9]/[AB]/run/$' {
			$config = @{ unitFile = (Join-Path $baseOf[$role] 'units\background.service'); peerRoot = [IO.Path]::GetFullPath((Join-Path $baseOf[$peerRole] '..\winunitd-qual')) }
			if ($Case -eq 'H14') {
				# A file only SYSTEM may read, and a path that does not exist.
				$denied = Join-Path $caseDir 'denied.txt'
				Set-Content -LiteralPath $denied -Value 'denied' -Encoding ASCII
				& icacls.exe $denied /inheritance:r /grant:r '*S-1-5-18:F' | Out-Null
				$config.absent = Join-Path $caseDir 'absent\file.txt'
				$config.denied = $denied
			}
			if ($Case -eq 'H17') { $config.loopback = '127.0.0.1:47017'; $config.peer = $PeerEcho }
			if ($Case -eq 'H18') { $config.smb = @{ server = $SmbServer; path = $SmbPath; expectSha256 = $SmbSha256 } }
			if ($Case -eq 'H19') { $config.efs = @{ path = $EfsPath; plain = $EfsPlain; expectSha256 = $EfsSha256 } }
			if ($Case -eq 'H15') { $config.pipe = '\\.\pipe\winunitd-qual\h15'; $config.winctl = $winctl; $config.outsideUnit = 'pipe-client.service' }
			$denial = [ordered]@{ 'system-only' = '\\.\pipe\winunitd-qual\system-only'; 'peer-user-pipe' = "\\.\pipe\winunitd-qual\workload\$($sidOf[$peerRole])"
				'control-pipe' = '\\.\pipe\winunitd\control'; 'maintenance-pipe' = '\\.\pipe\winunitd\maintenance' }
			if ($Case -eq 'H16') { $config.denyPipes = $denial }
			Set-ProbeConfig $role $config
			# The workload opens a configured loopback listener at its next
			# flush, five seconds at most.
			if ($Case -eq 'H17') { Start-Sleep -Seconds 6 }
			Start-Observer @('--duration', '600s') $report $marks $stop
			$evidence = [ordered]@{ tokenProbe = (Get-Probe $sid 'token').identity }
			switch ($Case) {
				'H13' { $evidence.paths = (Get-Probe $sid 'paths-own').paths }
				'H14' { $evidence.paths = (Get-Probe $sid 'paths-missing').paths }
				'H17' { $evidence.tcp = @((Get-Probe $sid 'tcp-loopback').tcp, (Get-Probe $sid 'tcp-peer').tcp) }
				'H18' { $evidence.smb = (Get-Probe $sid 'smb').smb }
				'H19' { $evidence.efs = (Get-Probe $sid 'efs').efs }
				'H15' {
					# Every client is an S4U process, so the fresh-boot phase
					# stays free of password logons: the in-unit probe, the
					# account's other unit, a stale claim and an exiting
					# caller from the unit, and the peer's own workload.
					$pipe = $config.pipe
					$serverOwn = Join-Path $caseDir 'pipe-server.json'
					$server = Start-Process -FilePath $Fixture -ArgumentList @('pipe-serve', '--name', $pipe, '--allow', $sid, '--report', $serverOwn, '--max', '4', '--duration', '300s') -PassThru -WindowStyle Hidden
					Start-Sleep -Seconds 2
					$results = @((Get-Probe $sid 'pipe').result)
					$outside = [IO.Path]::GetFullPath((Join-Path $baseOf[$role] '..\winunitd-qual\headless-workload\probes\outside-unit.json'))
					if (Test-Path -LiteralPath $outside) { Remove-Item -LiteralPath $outside }
					Invoke-Workload $sid @{ verb = 'probe'; probe = 'start-outside' } | Out-Null
					Wait-Until { Test-Path -LiteralPath $outside } 'the outside-unit client' 60
					$results += (Get-Content -LiteralPath $outside -Raw | ConvertFrom-Json).result
					$results += (Get-Probe $sid 'pipe-stale').result
					# The exiting caller writes nothing; the server's entry is
					# its evidence.
					try { Invoke-Workload $sid @{ verb = 'probe'; probe = 'pipe-exited' } | Out-Null } catch { $checks.Add('the exiting caller left no client report, as expected') }
					if (!$server.WaitForExit(300000)) { $checks.Add((Stop-ExactProcess $server 'the pipe server')) }
					$peerSid = $sidOf[$peerRole]
					$serverWide = Join-Path $caseDir 'pipe-server-wide.json'
					$wide = Start-Process -FilePath $Fixture -ArgumentList @('pipe-serve', '--name', "$pipe-wide", '--allow', $sid, '--acl', $sid, '--acl', $peerSid, '--report', $serverWide, '--max', '1', '--duration', '120s') -PassThru -WindowStyle Hidden
					Start-Sleep -Seconds 2
					Set-ProbeConfig $peerRole @{ pipe = "$pipe-wide" }
					$results += (Get-Probe $peerSid 'pipe-wrong-decision').result
					if (!$wide.WaitForExit(120000)) { $checks.Add((Stop-ExactProcess $wide 'the wide pipe server')) }
					$serverAcl = Join-Path $caseDir 'pipe-server-acl.json'
					$aclServer = Start-Process -FilePath $Fixture -ArgumentList @('pipe-serve', '--name', "$pipe-acl", '--allow', $sid, '--report', $serverAcl, '--max', '1', '--duration', '60s') -PassThru -WindowStyle Hidden
					Start-Sleep -Seconds 2
					Set-ProbeConfig $peerRole @{ pipe = "$pipe-acl" }
					$results += (Get-Probe $peerSid 'pipe-wrong-acl').result
					if (!$aclServer.WaitForExit(90000)) { $checks.Add((Stop-ExactProcess $aclServer 'the ACL pipe server')) }
					$evidence.pipe = $results
					$evidence.pipeServers = @(foreach ($f in @($serverOwn, $serverWide, $serverAcl)) { Get-Content -LiteralPath $f -Raw | ConvertFrom-Json })
				}
				'H16' {
					# SYSTEM identifies each pipe's live server, the account is
					# denied from its unit, SYSTEM identifies them again.
					$systemOnly = Start-Process -FilePath $Fixture -ArgumentList @('pipe-serve', '--name', $denial['system-only'], '--allow', 'S-1-5-18', '--acl', 'S-1-5-18',
						'--report', (Join-Path $caseDir 'pipe-server-system.json'), '--max', '8', '--duration', '300s') -PassThru -WindowStyle Hidden
					Start-Sleep -Seconds 2
					$health = @()
					foreach ($client in $denial.Keys) {
						$out = Join-Path $caseDir "endpoint-$client.json"
						Invoke-Native -File $Fixture -Arguments @('probe-endpoint', '--pipe', $denial[$client], '--client', $client, '--out', $out) | Out-Null
						$health += [ordered]@{ client = $client; pipe = $denial[$client]; before = (Get-Content -LiteralPath $out -Raw | ConvertFrom-Json).server }
					}
					$evidence.pipe = @()
					foreach ($client in $denial.Keys) { $evidence.pipe += (Get-Probe $sid "deny-$client").result }
					foreach ($h in $health) {
						$out = Join-Path $caseDir "endpoint-$($h.client)-after.json"
						Invoke-Native -File $Fixture -Arguments @('probe-endpoint', '--pipe', $h.pipe, '--client', $h.client, '--out', $out) | Out-Null
						$h.after = (Get-Content -LiteralPath $out -Raw | ConvertFrom-Json).server
					}
					if (!$systemOnly.WaitForExit(300000)) { $checks.Add((Stop-ExactProcess $systemOnly 'the SYSTEM-only pipe server')) }
					Add-Record "$key#pipe-health" 'control' (Get-HeadlessToken 'system' '' 0) ([ordered]@{ endpoints = $health })
				}
			}
			New-Item -ItemType File -Path $stop | Out-Null
			$observerReport = Wait-Observer $report 120
			$controls = switch ($Case) { 'H14' { @("$key#restore") } 'H16' { @("$key#pipe-health") } 'H17' { @("$key#peer-receipt") }
				'H18' { @("$key#password-share", "$key#server-principal") } 'H19' { @("$key#password-decrypt") } default { @() } }
			Add-Record $key 'primary' (Get-HeadlessToken 's4u' $sid 0) $evidence $controls $report
			break
		}
		# Controls run on their own, in their own phase.
		'^H14/[AB]/run/restore$' {
			Set-ProbeConfig $role @{ unitFile = (Join-Path $baseOf[$role] 'units\background.service'); peerRoot = [IO.Path]::GetFullPath((Join-Path $baseOf[$peerRole] '..\winunitd-qual')) }
			Start-Observer @('--duration', '300s') $report $marks $stop
			$evidence = [ordered]@{ tokenProbe = (Get-Probe $sid 'token').identity; paths = (Get-Probe $sid 'paths-own').paths }
			New-Item -ItemType File -Path $stop | Out-Null
			$observerReport = Wait-Observer $report 120
			Add-Record $key 'control' (Get-HeadlessToken 's4u' $sid 0) $evidence @() $report
			break
		}
		'^H17/[AB]/run/peer-receipt$' {
			Add-Record $key 'control' (Get-HeadlessToken 'peer' '' 0) ([ordered]@{ receipt = (Get-Content -LiteralPath $PeerReceipt -Raw | ConvertFrom-Json) })
			break
		}
		'^H18/[AB]/run/server-principal$' {
			Add-Record $key 'control' (Get-HeadlessToken 'peer' '' 0) ([ordered]@{ server = (Get-Content -LiteralPath $PeerAudit -Raw | ConvertFrom-Json) })
			break
		}
		'^(H18/[AB]/run/password-share|H19/B/run/password-decrypt)$' {
			$config = Join-Path $caseDir 'password-config.json'
			if ($Case -eq 'H18') { @{ smb = @{ server = $SmbServer; path = $SmbPath; expectSha256 = $SmbSha256 } } | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath $config -Encoding UTF8 }
			else { @{ efs = @{ path = $EfsPath; plain = $EfsPlain; expectSha256 = $EfsSha256 } } | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath $config -Encoding UTF8 }
			$out = Join-Path $caseDir 'password-probe.json'
			$probe = if ($Case -eq 'H18') { 'probe-smb' } else { 'probe-efs' }
			Invoke-AsPassword $role @($probe, '--config', $config, '--out', $out)
			$rep = Get-Content -LiteralPath $out -Raw | ConvertFrom-Json
			$evidence = [ordered]@{ tokenProbe = $rep.identity }
			if ($Case -eq 'H18') { $evidence.smb = $rep.smb } else { $evidence.efs = $rep.efs }
			Add-Record $key 'control' (Get-HeadlessToken 'password' $sid ([int]$rep.identity.session)) $evidence
			break
		}
		# Final cleanup: restore what the qualification changed, then
		# SYSTEM's inventory against the baseline receipt.
		'^H22/' {
			foreach ($r in @('A', 'B', 'admin')) { if (Test-Path -LiteralPath (Join-Path $Linger $sidOf[$r])) { Set-Linger $r $false } }
			Wait-Until { @(@($SidA, $SidB, $AdminSid) | ForEach-Object { Get-UserManagerPids $_ }).Count -eq 0 } 'the user managers to exit' 180
			Get-ScheduledTask -TaskPath '\winunitd-qual\' -ErrorAction SilentlyContinue | Unregister-ScheduledTask -Confirm:$false
			Restore-AdmissionBaseline
			# The Default profile template had no WinUnit files at the
			# baseline unless the receipt says so.
			if (@((Get-Content -LiteralPath $BaselineReceipt -Raw | ConvertFrom-Json).facts.PSObject.Properties.Name) -notcontains 'templateFiles') {
				$default = [Environment]::ExpandEnvironmentVariables((Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList').Default)
				$template = Join-Path $default 'AppData\Local\winunitd'
				if (Test-Path -LiteralPath $template) { Remove-Item -LiteralPath $template -Recurse -Force }
			}
			$out = Join-Path $caseDir 'inventory.json'
			$images = @($manifest.artifacts | ForEach-Object { @('--image', $_.name) })
			Invoke-Native -File $Fixture -Arguments (@('inventory', '--baseline', $BaselineReceipt) + $inventoryScope + $images + @('--out', $out)) | Out-Null
			Add-Record $key 'primary' (Get-HeadlessToken 'system' '' 0) ([ordered]@{ inventory = (Get-Content -LiteralPath $out -Raw | ConvertFrom-Json) })
			break
		}
		default { throw "This driver has no procedure for $key at stage $Stage" }
	}
} catch {
	$failures.Add($_.Exception.Message)
} finally {
	# Undo exactly what this execution changed.
	foreach ($r in @($sessionOpen)) { try { Invoke-Wts 'logoff' $r } catch { $cleanup = $false; $failures.Add("logoff: $($_.Exception.Message)") } }
	if ($brokerStopped) { try { Start-Broker } catch { $cleanup = $false; $failures.Add("broker start: $($_.Exception.Message)") } }
	foreach ($r in @($lingerChanged)) { try { Set-Linger $r $true } catch { $cleanup = $false; $failures.Add("linger restore: $($_.Exception.Message)") } }
	if ($null -ne $script:observer -and !$script:observer.HasExited) {
		if (!(Test-Path -LiteralPath $stop) -and $caseCreated) { New-Item -ItemType File -Path $stop -ErrorAction SilentlyContinue | Out-Null }
		if (!$script:observer.WaitForExit(120000)) { $cleanup = $false; $failures.Add((Stop-ExactProcess $script:observer 'the observer')) }
	}
	if ($transcript) { Stop-Transcript | Out-Null }
}

# Records: one observation per record, then headless-workload record.
$bootId = Get-BootId
$next = $Sequence
foreach ($r in $records) {
	$result = if ($failures.Count -eq 0) { 'pass' } else { 'fail' }
	$recordBoot = $bootId
	if ($r.Observer) {
		$obs = Get-Content -LiteralPath $r.Observer -Raw | ConvertFrom-Json
		$recordBoot = "boot-$($obs.boot.counter)-$($obs.boot.time)"
	}
	$seq = $next
	if ($Case -notin @('H01', 'H02', 'H03', 'H09') -or $r.Kind -eq 'control') { $next++ }
	$detail = Protect-Detail ((@($checks) + @($failures)) -join '; ') $pairs
	if ($detail.Length -gt 500) { $detail = $detail.Substring(0, 500) }
	$executionId = if ($r.Kind -eq 'control') { "$ExecutionId-$(($r.Key -split '#')[1])" } else { $ExecutionId }
	$observation = New-HeadlessObservation -Key $r.Key -Kind $r.Kind -Result $result -Token $r.Token -ExecutionId $executionId -Sequence $seq -BootId $recordBoot `
		-PasswordLogons 0 -RunnerId $r.Runner -Cleanup $cleanup -Controls $r.Controls -Evidence $r.Evidence -Detail $detail
	$path = Join-Path $CaseRoot ("observation-" + ($r.Key -replace '[/#]', '-') + '.json')
	$observation | ConvertTo-Json -Depth 32 | Set-Content -LiteralPath $path -Encoding UTF8
	$recordArgs = @('record', '--observation', $path, '--results', $Results, '--admission', $Admission)
	if ($r.Observer) { $recordArgs += @('--observer', $r.Observer) }
	try { Invoke-Native -File $Fixture -Arguments $recordArgs | Out-Null } catch { Write-Output "record: $(Protect-Detail $_.Exception.Message $pairs)" }
}
if ($Case -in @('H01', 'H02', 'H03', 'H09') -and $records.Count -gt 0) { $next++ }
Write-Output "next-sequence $next"
if ($failures.Count -gt 0) { exit 1 }
