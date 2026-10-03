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
		'^(A|s4u-A|linger-A|stop-A|revocation|shutdown|deadline|group|security-A|legacy-repair)$' { 'A'; break }
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

# The account's one interactive session among an observer report's
# session samples. None, or more than one, is an error: an interactive
# record never claims session zero.
function Get-ObservedSession($Report, [string]$Sid) {
	$users = @()
	if ($null -ne $Report -and $Report.PSObject.Properties['sessions']) {
		foreach ($sample in @($Report.sessions)) { if ($sample.PSObject.Properties['users']) { $users += @($sample.users) } }
	}
	$ids = @($users | Where-Object { $_.sid -eq $Sid -and [int]$_.session -gt 0 } | ForEach-Object { [int]$_.session } | Sort-Object -Unique)
	if ($ids.Count -ne 1) { throw "The observer saw $($ids.Count) interactive sessions of the account" }
	return $ids[0]
}

# The session a native test receipt ran in: the recorder's and the test
# process's own token, the account's, in one positive session.
function Get-ReceiptSession($Receipt, [string]$Sid) {
	$o, $r = $Receipt.owner.token, $Receipt.runner.token
	if ($o.sid -ne $Sid -or $r.sid -ne $Sid -or [int]$o.session -le 0 -or [int]$o.session -ne [int]$r.session) {
		throw 'The test receipt names no interactive session of the account'
	}
	return [int]$o.session
}

# The token header a record claims: session zero for S4U and SYSTEM rows,
# and for interactive rows the account's own session as the observer
# report or the test receipt recorded it.
function Get-RecordToken([string]$Mode, [string]$Sid, $Report = $null, $Receipt = $null) {
	if ($Mode -notin @('wts', 'filtered-admin')) { return Get-HeadlessToken $Mode $Sid 0 }
	$observed = if ($null -ne $Receipt) { Get-ReceiptSession $Receipt $Sid } elseif ($null -ne $Report) { Get-ObservedSession $Report $Sid } else {
		throw 'An interactive record needs the session it ran in'
	}
	return Get-HeadlessToken $Mode $Sid $observed
}

# The named native tests of a variant.
function Get-NativeTests([string]$Variant) {
	$tests = @{ revocation = 'TestLingerRevocationOverlapsNativeHeadlessManagerCreation'; shutdown = 'TestShutdownOverlapsNativeHeadlessManagerCreation'
		deadline = 'TestShutdownDeadlineOverlapsNativeHeadlessManagerCreation'; 'security-A' = 'TestNativeHeadlessUserManagerSecurity'
		'security-B' = 'TestNativeHeadlessUserManagerSecurity'; 'sensitivity-A' = 'TestNativeSecurityProbeDetectsSelectiveInheritance'
		'sensitivity-B' = 'TestNativeSecurityProbeDetectsSelectiveInheritance'
		'go-tests' = @('TestDaemonPathSDDL', 'TestDaemonPathOwnerAllowed', 'TestDaemonLogNonAdminIdentity') }
	if (!$tests.ContainsKey($Variant)) { throw "No native tests for $Variant" }
	return @($tests[$Variant])
}

# One H20 repetition: the three held-launch tests, a record each, run by
# one recorder, so the repetition's runner is one real process.
function Get-NativeTestGroup([string]$Repetition) {
	if ($Repetition -notmatch '^r[1-5]$') { throw 'H20 runs as one repetition, r1 to r5' }
	$variants = @('revocation', 'shutdown', 'deadline')
	return [pscustomobject]@{
		Runner   = "runner-$Repetition"
		Variants = $variants
		Tests    = @($variants | ForEach-Object { Get-NativeTests $_ })
		Keys     = @($variants | ForEach-Object { Get-HeadlessKey 'H20' $_ $Repetition '' })
	}
}

# The file `headless-workload record` writes for a key.
function Get-ResultFileName([string]$Key) { return $Key.Replace('/', '_').Replace('#', '+') + '.json' }

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

# A whole Windows command line, each argument quoted on its own, so a path
# with spaces stays one argument.
function Format-NativeCommandLine([string[]]$Arguments) { return (@($Arguments) | ForEach-Object { ConvertTo-NativeArgument $_ }) -join ' ' }
#endregion

#region tested helpers: files, children, replies and recording
# JSON that native code reads, as UTF-8 without a byte-order mark: the
# fixture's strict decoder refuses one, and Windows PowerShell 5.1 writes it
# for -Encoding UTF8.
function Write-JsonFile([string]$Path, $Value, [int]$Depth = 32) {
	[IO.File]::WriteAllText($Path, (ConvertTo-Json -InputObject $Value -Depth $Depth), (New-Object Text.UTF8Encoding $false))
}

# One line from a reader within a deadline: a reply that does not arrive
# is an error, never a hang that keeps the finally block from running.
function Read-LineWithin([IO.TextReader]$Reader, [int]$Milliseconds) {
	$task = $Reader.ReadLineAsync()
	if (!$task.Wait($Milliseconds)) { throw "No reply within $Milliseconds ms" }
	return $task.Result
}

function Stop-ExactProcess([Diagnostics.Process]$Process, [string]$What) {
	try { if (!$Process.HasExited) { $Process.Kill() } } catch { }
	if ($Process.WaitForExit(30000)) { return "$What was killed after its deadline" }
	return "$What did not exit after kill: cleanup unconfirmed, case evidence kept"
}

# Children this execution starts besides the observer, such as pipe
# servers: registered as they start, so the finally block stops and reaps
# each one however the execution ended.
$script:children = New-Object Collections.Generic.List[Diagnostics.Process]
function Start-Child([string]$File, [string[]]$Arguments) {
	$start = New-Object Diagnostics.ProcessStartInfo
	$start.FileName = $File
	$start.Arguments = Format-NativeCommandLine $Arguments
	$start.UseShellExecute = $false
	$start.CreateNoWindow = $true
	$child = [Diagnostics.Process]::Start($start)
	$script:children.Add($child)
	return $child
}

# Wait for a registered child to finish on its own within a deadline.
# Returns '' when it did; otherwise kills it and returns the failure, which
# is a cleanup failure when the child did not exit even after the kill.
function Wait-Child([Diagnostics.Process]$Child, [int]$Milliseconds, [string]$What) {
	if ($Child.WaitForExit($Milliseconds)) { return '' }
	return Stop-ExactProcess $Child $What
}

# Stops every registered child still running; returns the failures.
function Stop-Children([int]$GraceMs = 5000) {
	$problems = @()
	foreach ($child in $script:children) {
		if ($child.HasExited) { continue }
		$state = Wait-Child $child $GraceMs 'a child process'
		if ($state) { $problems += $state }
	}
	return , @($problems)
}

# Records: one observation per record, written as JSON native code reads,
# recorded by Record, confirmed by the new record file the recorder
# writes. Returns the next run-wide sequence and every record failure; a
# record that is not admitted fails the execution.
function Complete-HeadlessRun {
	param($Records, [string]$Case, [int]$Sequence, [string]$ExecutionId, [string]$BootId, [bool]$Passed, [bool]$Cleanup, [string]$Detail,
		[string]$CaseRoot, [string]$Results, [scriptblock]$Record, [scriptblock]$ObserverBoot)
	$next = $Sequence
	$failed = @()
	$shared = $Case -in @('H01', 'H02', 'H03', 'H09')
	foreach ($r in $Records) {
		$seq = $next
		if (!$shared -or $r.Kind -eq 'control') { $next++ }
		try {
			$recordBoot = if ($r.Observer) { & $ObserverBoot $r.Observer } else { $BootId }
			$executionId = if ($r.Kind -eq 'control') { "$ExecutionId-$(($r.Key -split '#')[1])" } elseif ($r['ExecutionId']) { $r['ExecutionId'] } else { $ExecutionId }
			$result = if ($Passed) { 'pass' } else { 'fail' }
			$observation = New-HeadlessObservation -Key $r.Key -Kind $r.Kind -Result $result -Token $r.Token -ExecutionId $executionId -Sequence $seq -BootId $recordBoot `
				-PasswordLogons 0 -RunnerId $r.Runner -Cleanup $Cleanup -Controls $r.Controls -Evidence $r.Evidence -Detail $Detail
			$path = Join-Path $CaseRoot ("observation-" + ($r.Key -replace '[/#]', '-') + '.json')
			$written = Join-Path $Results (Get-ResultFileName $r.Key)
			if (Test-Path -LiteralPath $written) { throw 'a record for this key already exists' }
			Write-JsonFile $path $observation
			$recordArgs = @('record', '--observation', $path, '--results', $Results)
			if ($r.Observer) { $recordArgs += @('--observer', $r.Observer) }
			& $Record $recordArgs
			if (!(Test-Path -LiteralPath $written)) { throw 'the recorder wrote no record' }
		} catch {
			$failed += "record $($r.Key): $($_.Exception.Message)"
		}
	}
	if ($shared -and @($Records).Count -gt 0) { $next++ }
	return [pscustomobject]@{ Next = $next; Failures = @($failed) }
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

# icacls with its result checked at once; its output names paths, so it
# stays out of the error.
function Invoke-Icacls([string[]]$Arguments) {
	& icacls.exe @Arguments 2>&1 | Out-Null
	if ($LASTEXITCODE -ne 0) { throw "icacls failed ($LASTEXITCODE)" }
}

# Wait until a pipe server's pipe exists, listing pipes rather than
# connecting, so no instance is used up; a server that exits first fails.
function Wait-PipeReady([string]$Pipe, [Diagnostics.Process]$Server, [int]$Seconds = 30) {
	$deadline = (Get-Date).AddSeconds($Seconds)
	while (@([IO.Directory]::GetFiles('\\.\pipe\')) -notcontains $Pipe) {
		if ($Server.HasExited) { throw "The pipe server exited before serving ($($Server.ExitCode))" }
		if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for the pipe server' }
		Start-Sleep -Milliseconds 100
	}
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
	Write-JsonFile $tmp ([ordered]@{ mode = $policy.mode; users = $users }) 4
	Invoke-Icacls @($tmp, '/inheritance:r', '/grant:r', '*S-1-5-18:F', '*S-1-5-32-544:F', '*S-1-5-32-545:R')
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

# test-receipt as SYSTEM with the native test fixture's environment; it
# hands the test its subject report path itself.
function Invoke-NativeTestRecorder([string[]]$Arguments, [int]$TimeoutMs) {
	$env:WINUNITD_NATIVE_OVERLAP_HEADLESS_SID = $sid
	$env:WINUNITD_NATIVE_OVERLAP_FIXTURE = 'disposable'
	$env:WINUNITD_NATIVE_SECURITY_OTHER_SID = $sidOf[$peerRole]
	try {
		Invoke-Native -File $Fixture -Arguments $Arguments -TimeoutMs $TimeoutMs | Out-Null
	} finally {
		foreach ($name in @('WINUNITD_NATIVE_OVERLAP_HEADLESS_SID', 'WINUNITD_NATIVE_OVERLAP_FIXTURE', 'WINUNITD_NATIVE_SECURITY_OTHER_SID')) {
			Remove-Item "Env:$name" -ErrorAction SilentlyContinue
		}
	}
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
		$line = Read-LineWithin $reader 180000
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
	Write-JsonFile (Join-Path $state 'config.json') $Config 4
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

function Add-Record([string]$RecordKey, [string]$Kind, $Token, $Evidence, [string[]]$Controls = @(), [string]$Observer = '', [string]$Runner = '',
	[string]$Execution = '') {
	$records.Add([ordered]@{ Key = $RecordKey; Kind = $Kind; Token = $Token; Evidence = $Evidence; Controls = $Controls; Observer = $Observer; Runner = $Runner
			ExecutionId = $Execution })
}

# A child that must finish on its own: one that outlives its deadline fails
# the execution, and one that survives the kill leaves cleanup unconfirmed.
function Confirm-Child([Diagnostics.Process]$Child, [int]$Milliseconds, [string]$What) {
	$state = Wait-Child $Child $Milliseconds $What
	if ($state) {
		$failures.Add($state)
		if ($state -match 'cleanup unconfirmed') { $script:cleanup = $false }
	}
}

try {
	if (Test-Path -LiteralPath $caseDir) { throw 'Fresh case directory required' }
	New-Item -ItemType Directory -Path $caseDir | Out-Null
	$caseCreated = $true
	# SYSTEM, Administrators and the qualification accounts, which write
	# probe outputs here through the lab commands.
	$grants = @('*S-1-5-18:(OI)(CI)F', '*S-1-5-32-544:(OI)(CI)F')
	foreach ($s in @($SidA, $SidB, $AdminSid)) { if ($s) { $grants += "*${s}:(OI)(CI)M" } }
	Invoke-Icacls (@($caseDir, '/inheritance:r', '/grant:r') + $grants)
	New-Item -ItemType Directory -Path $marks | Out-Null
	Start-Transcript -LiteralPath (Join-Path $caseDir 'driver.log') | Out-Null
	$transcript = $true

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
			Wait-Until { $r = Read-ObserverReport $bootReport; $null -ne $r -and $r.stage -eq 'finished' } 'the cold-boot observer' $TimeoutSeconds
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
			Wait-Until { $r = Read-ObserverReport $bootReport; $null -ne $r -and $r.stage -eq 'finished' } 'the no-grant observer' $TimeoutSeconds
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
			Add-Record $key 'primary' (Get-RecordToken $caseInfo.Mode $sid $observerReport) $null @() $report
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
			# The session samples keep the account's session from before a
			# logoff, so the header names the session the execution used.
			Add-Record $key 'primary' (Get-RecordToken $caseInfo.Mode $sid $observerReport) $null @() $report
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
				Invoke-Icacls @($dir, '/setowner', "*$sid")
				Invoke-Icacls @($dir, '/reset')
				Invoke-Icacls @($dir, '/grant', "*${sid}:(OI)(CI)F", '*S-1-5-32-545:(OI)(CI)F')
			} else {
				Invoke-Native -File $Fixture -Arguments @('pad-log', '--path', (Join-Path $root 'daemon\daemon.log')) | Out-Null
			}
			Invoke-Native -File $Fixture -Arguments @('probe-daemon-log', '--root', $root, '--sid', $sid, '--phase', 'before', '--out', $before) | Out-Null
			Start-Observer @('--duration', '120s') $report '' '' @('A', 'B', 'admin')
			Start-Broker; $brokerStopped = $false
			$observerReport = Wait-Observer $report 200
			Invoke-Native -File $Fixture -Arguments @('probe-daemon-log', '--root', $root, '--sid', $sid, '--phase', 'after', '--before', $before, '--out', $after) | Out-Null
			Add-Record $key 'primary' (Get-RecordToken $caseInfo.Mode $sid $observerReport) ([ordered]@{ daemonLog = (Get-Content -LiteralPath $after -Raw | ConvertFrom-Json) }) @() $report
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
			Write-JsonFile $config @{ ownDaemon = $BaseB; peerDaemon = $BaseA; systemDaemon = $DataDir }
			$token = Join-Path $caseDir 'token.json'
			$paths = Join-Path $caseDir 'paths.json'
			Invoke-InSession $role $Fixture @('probe-token', '--out', $token)
			Invoke-InSession $role $Fixture @('probe-path', '--set', 'daemon-denial', '--config', $config, '--out', $paths)
			$tp = (Get-Content -LiteralPath $token -Raw | ConvertFrom-Json).identity
			Add-Record $key 'primary' (Get-HeadlessToken 'wts' $sid ([int]$tp.session)) ([ordered]@{ tokenProbe = $tp; paths = (Get-Content -LiteralPath $paths -Raw | ConvertFrom-Json).paths })
			break
		}
		# Named native tests. test-receipt runs the test binary as its child
		# in a kill-on-close job and records itself, the test process, the
		# exit code and the test2json events. An H20 repetition runs its
		# three held-launch tests under one recorder, each in its own test
		# process with its own receipt and subject report.
		'^H20/group/run/$' {
			$group = Get-NativeTestGroup $Repetition
			$dir = Join-Path $caseDir 'receipts'
			New-Item -ItemType Directory -Path $dir | Out-Null
			$receiptArgs = @('test-receipt', '--test2json', $Test2Json, '--artifact', (Split-Path -Leaf $TestBinary), '--binary', $TestBinary,
				'--runner', $group.Runner, '--out-dir', $dir, '--subjects')
			foreach ($test in $group.Tests) { $receiptArgs += @('--each', $test) }
			Invoke-NativeTestRecorder $receiptArgs 5400000
			for ($i = 0; $i -lt $group.Keys.Count; $i++) {
				$receipt = Get-Content -LiteralPath (Join-Path $dir "$($group.Tests[$i]).json") -Raw | ConvertFrom-Json
				Add-Record $group.Keys[$i] 'primary' (Get-RecordToken 's4u' $sid) ([ordered]@{ testRun = $receipt }) @() '' $group.Runner "$ExecutionId-$($group.Variants[$i])"
			}
			break
		}
		'^H20/' { throw 'H20 runs a repetition''s three tests under one recorder: use -Variant group' }
		'^(H21|G6/go-tests)' {
			$run = '^(' + (@(Get-NativeTests $Variant) -join '|') + ')$'
			$events = Join-Path $caseDir 'test.json'
			$subject = Join-Path $caseDir 'subject.json'
			$receiptPath = Join-Path $caseDir 'receipt.json'
			$runner = "runner-$Variant"
			$receiptArgs = @('test-receipt', '--run', $run, '--test2json', $Test2Json, '--events', $events, '--artifact', (Split-Path -Leaf $TestBinary),
				'--binary', $TestBinary, '--runner', $runner, '--out', $receiptPath)
			if ($Variant -eq 'go-tests') {
				# The account's own interactive token runs them, so the
				# identity test cannot pass by skipping.
				Invoke-Wts 'logon' $role; $sessionOpen += $role
				Invoke-InSession $role $Fixture $receiptArgs
			} else {
				if ($caseInfo.Mode -eq 's4u') { $receiptArgs += @('--subject', $subject) }
				Invoke-NativeTestRecorder $receiptArgs 1800000
			}
			$receipt = Get-Content -LiteralPath $receiptPath -Raw | ConvertFrom-Json
			Add-Record $key 'primary' (Get-RecordToken $caseInfo.Mode $sid $null $receipt) ([ordered]@{ testRun = $receipt }) @() '' $runner
			break
		}
		# Probes from the workload in its unit, with the observer holding it.
		'^H1[3-9]/[AB]/run/$' {
			$config = @{ unitFile = (Join-Path $baseOf[$role] 'units\background.service'); peerRoot = [IO.Path]::GetFullPath((Join-Path $baseOf[$peerRole] '..\winunitd-qual')) }
			if ($Case -eq 'H14') {
				# A file only SYSTEM may read, and a path that does not exist.
				$denied = Join-Path $caseDir 'denied.txt'
				Set-Content -LiteralPath $denied -Value 'denied' -Encoding ASCII
				Invoke-Icacls @($denied, '/inheritance:r', '/grant:r', '*S-1-5-18:F')
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
					$server = Start-Child $Fixture @('pipe-serve', '--name', $pipe, '--allow', $sid, '--report', $serverOwn, '--max', '4', '--duration', '300s')
					Wait-PipeReady $pipe $server
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
					Confirm-Child $server 330000 'the pipe server' 
					$peerSid = $sidOf[$peerRole]
					$serverWide = Join-Path $caseDir 'pipe-server-wide.json'
					$wide = Start-Child $Fixture @('pipe-serve', '--name', "$pipe-wide", '--allow', $sid, '--acl', $sid, '--acl', $peerSid, '--report', $serverWide, '--max', '1', '--duration', '120s')
					Wait-PipeReady "$pipe-wide" $wide
					Set-ProbeConfig $peerRole @{ pipe = "$pipe-wide" }
					$results += (Get-Probe $peerSid 'pipe-wrong-decision').result
					Confirm-Child $wide 150000 'the wide pipe server' 
					$serverAcl = Join-Path $caseDir 'pipe-server-acl.json'
					$aclServer = Start-Child $Fixture @('pipe-serve', '--name', "$pipe-acl", '--allow', $sid, '--report', $serverAcl, '--max', '1', '--duration', '60s')
					Wait-PipeReady "$pipe-acl" $aclServer
					Set-ProbeConfig $peerRole @{ pipe = "$pipe-acl" }
					$results += (Get-Probe $peerSid 'pipe-wrong-acl').result
					Confirm-Child $aclServer 90000 'the ACL pipe server' 
					$evidence.pipe = $results
					$evidence.pipeServers = @(foreach ($f in @($serverOwn, $serverWide, $serverAcl)) { Get-Content -LiteralPath $f -Raw | ConvertFrom-Json })
				}
				'H16' {
					# SYSTEM identifies each pipe's live server, the account is
					# denied from its unit, SYSTEM identifies them again.
					$systemOnly = Start-Child $Fixture @('pipe-serve', '--name', $denial['system-only'], '--allow', 'S-1-5-18', '--acl', 'S-1-5-18',
						'--report', (Join-Path $caseDir 'pipe-server-system.json'), '--max', '8', '--duration', '300s')
					Wait-PipeReady $denial['system-only'] $systemOnly
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
					Confirm-Child $systemOnly 330000 'the SYSTEM-only pipe server' 
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
			if ($Case -eq 'H18') { Write-JsonFile $config @{ smb = @{ server = $SmbServer; path = $SmbPath; expectSha256 = $SmbSha256 } } 3 }
			else { Write-JsonFile $config @{ efs = @{ path = $EfsPath; plain = $EfsPlain; expectSha256 = $EfsSha256 } } 3 }
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
	# Undo exactly what this execution changed, its children first.
	foreach ($state in (Stop-Children)) {
		$failures.Add($state)
		if ($state -match 'cleanup unconfirmed') { $cleanup = $false }
	}
	foreach ($r in @($sessionOpen)) { try { Invoke-Wts 'logoff' $r } catch { $cleanup = $false; $failures.Add("logoff: $($_.Exception.Message)") } }
	if ($brokerStopped) { try { Start-Broker } catch { $cleanup = $false; $failures.Add("broker start: $($_.Exception.Message)") } }
	foreach ($r in @($lingerChanged)) { try { Set-Linger $r $true } catch { $cleanup = $false; $failures.Add("linger restore: $($_.Exception.Message)") } }
	if ($null -ne $script:observer -and !$script:observer.HasExited) {
		if (!(Test-Path -LiteralPath $stop) -and $caseCreated) { New-Item -ItemType File -Path $stop -ErrorAction SilentlyContinue | Out-Null }
		if (!$script:observer.WaitForExit(120000)) { $cleanup = $false; $failures.Add((Stop-ExactProcess $script:observer 'the observer')) }
	}
	if ($transcript) { Stop-Transcript | Out-Null }
}

# Records: one observation per record, each admitted by headless-workload
# record. A record that is not admitted fails the execution, and the next
# sequence is reported only when every record was admitted.
$detail = Protect-Detail ((@($checks) + @($failures)) -join '; ') $pairs
if ($detail.Length -gt 500) { $detail = $detail.Substring(0, 500) }
$bootId = Get-BootId
$tail = Complete-HeadlessRun -Records $records -Case $Case -Sequence $Sequence -ExecutionId $ExecutionId -BootId $bootId -Passed ($failures.Count -eq 0) `
	-Cleanup $cleanup -Detail $detail -CaseRoot $CaseRoot -Results $Results `
	-Record { param([string[]]$RecordArgs) Invoke-Native -File $Fixture -Arguments ($RecordArgs + @('--admission', $Admission)) | Out-Null } `
	-ObserverBoot { param([string]$Path) $obs = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json; "boot-$($obs.boot.counter)-$($obs.boot.time)" }
foreach ($f in $tail.Failures) { $failures.Add((Protect-Detail $f $pairs)) }
if ($tail.Failures.Count -eq 0) { Write-Output "next-sequence $($tail.Next)" }
if ($failures.Count -gt 0) {
	Write-Output "failed: $(Protect-Detail ($failures -join '; ') $pairs)"
	exit 1
}
