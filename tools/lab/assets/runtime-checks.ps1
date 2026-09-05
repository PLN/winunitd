param([switch]$DisposableLab)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (!$DisposableLab) { throw 'Explicit disposable-lab acknowledgement required' }
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'Expected SYSTEM' }
if (Get-NetRoute -DestinationPrefix @('0.0.0.0/0', '::/0') -ErrorAction SilentlyContinue) { throw 'Qualification requires disconnected maintenance routing' }
$root = 'C:\winunitd-lab'
$service = Get-CimInstance Win32_Service -Filter "Name='winunitd'"
if (!$service -or $service.PathName -notmatch '^"?C:\\winunitd-lab\\winunitd\.exe"?\s') { throw 'Expected isolated lab service installation' }
$manifest = Get-Content "$root\build-manifest.json" -Raw | ConvertFrom-Json
foreach ($artifact in $manifest.artifacts) {
	if ((Get-FileHash (Join-Path $root $artifact.name)).Hash -ne $artifact.sha256) { throw 'Artifact hash mismatch' }
}
$names = @('r1-output.service', 'r1-reload.service', 'r1-notify.service')
foreach ($name in $names) {
	if (Test-Path -LiteralPath "$root\data\units\$name") { throw 'Fresh runtime-check namespace required' }
}
function Invoke-Control {
	param([string[]]$Command, [int[]]$Expected = @(0))
	# These fixtures use only arguments without whitespace or quotes.
	if ($Command | Where-Object { $_ -match '[\s"]' }) { throw 'Unsupported fixture argument' }
	$start = New-Object Diagnostics.ProcessStartInfo
	$start.FileName = "$root\winctl.exe"
	$start.Arguments = $Command -join ' '
	$start.UseShellExecute = $false
	$start.CreateNoWindow = $true
	$start.RedirectStandardOutput = $true
	$start.RedirectStandardError = $true
	$process = [Diagnostics.Process]::Start($start)
	try {
		$output = $process.StandardOutput.ReadToEndAsync()
		$errors = $process.StandardError.ReadToEndAsync()
		if (!$process.WaitForExit(60000)) { $process.Kill(); throw 'Control command exceeded fixture deadline' }
		$text = $output.GetAwaiter().GetResult()
		$errorText = $errors.GetAwaiter().GetResult()
		if ($process.ExitCode -notin $Expected) { throw "Control command failed: $($Command -join ' '): $errorText" }
		return $text
	} finally { $process.Dispose() }
}
function Write-FixtureUnit {
	param([string]$Name, [string]$Type, [string]$Script)
	@"
[Service]
Type=$Type
ExecStart=C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe
ExecStartArg=-NoProfile
ExecStartArg=-ExecutionPolicy
ExecStartArg=Bypass
ExecStartArg=-File
ExecStartArg=$root\$Script
WorkingDirectory=$root
TimeoutStartSec=30s
TimeoutStopSec=10s
"@ | Set-Content -LiteralPath "$root\data\units\$Name" -Encoding ASCII
}
Start-Transcript "$root\runtime-checks.log"
$passed = @()
try {
	Start-Service winunitd
	$deadline = (Get-Date).AddSeconds(45)
	do {
		try { Invoke-Control @('list-units') | Out-Null; break } catch {
			if ((Get-Date) -ge $deadline) { throw }; Start-Sleep 1
		}
	} while ($true)
	@'
for ($i = 0; $i -lt 5000; $i++) {
	[Console]::Out.WriteLine("out-$i")
	[Console]::Error.WriteLine("err-$i")
}
[Console]::Out.Write(('x' * 131073))
'@ | Set-Content "$root\r1-output.ps1" -Encoding ASCII
	Write-FixtureUnit 'r1-output.service' 'oneshot' 'r1-output.ps1'
	Invoke-Control @('daemon-reload') | Out-Null
	Invoke-Control @('start', 'r1-output.service') | Out-Null
	$logs = Invoke-Control @('logs', 'r1-output.service')
	foreach ($prefix in @('out', 'err')) {
		$matches = [regex]::Matches($logs, "(?m) $prefix-(\d+)\r?$")
		if ($matches.Count -ne 5000) { throw "Missing $prefix records: $($matches.Count)" }
		$distinct = @($matches | ForEach-Object { [int]$_.Groups[1].Value } | Sort-Object -Unique)
		if ($distinct.Count -ne 5000 -or $distinct[0] -ne 0 -or $distinct[-1] -ne 4999) { throw 'Output sequence mismatch' }
	}
	$fragments = [regex]::Matches($logs, '(?m): (x+)\r?$')
	$length = 0
	foreach ($fragment in $fragments) { $length += $fragment.Groups[1].Length }
	if ($length -ne 131073 -or $fragments.Count -lt 3) { throw 'Unterminated output fragment loss' }
	$passed += 'oneshot-5000-stdout-5000-stderr-and-unterminated-output'

	'while ($true) { Write-Output "reload-heartbeat"; Start-Sleep 1 }' | Set-Content "$root\r1-reload.ps1" -Encoding ASCII
	Write-FixtureUnit 'r1-reload.service' 'simple' 'r1-reload.ps1'
	$definition = Get-Content "$root\data\units\r1-reload.service" -Raw
	Invoke-Control @('daemon-reload') | Out-Null
	Invoke-Control @('start', 'r1-reload.service') | Out-Null
	$before = Invoke-Control @('status', 'r1-reload.service')
	$invocation = [regex]::Match($before, 'InvocationID=(\S+)').Groups[1].Value
	$fixturePID = [int][regex]::Match($before, 'Main PID: (\d+)').Groups[1].Value
	if (!$invocation -or !$fixturePID) { throw 'Missing live invocation identity' }
	Remove-Item -LiteralPath "$root\data\units\r1-reload.service"
	Invoke-Control @('daemon-reload') | Out-Null
	$missing = Invoke-Control @('status', 'r1-reload.service')
	if ($missing -notmatch 'Loaded: unavailable' -or !$missing.Contains("InvocationID=$invocation")) { throw 'Reload lost live unit ownership' }
	Get-Process -Id $fixturePID -ErrorAction Stop | Out-Null
	Start-Sleep 2
	if ((Invoke-Control @('logs', 'r1-reload.service')) -notmatch 'reload-heartbeat') { throw 'Retained unit logs unavailable' }
	Invoke-Control @('stop', 'r1-reload.service') | Out-Null
	if (Get-Process -Id $fixturePID -ErrorAction SilentlyContinue) { throw 'Retained fixture survived stop' }
	$definition | Set-Content "$root\data\units\r1-reload.service" -Encoding ASCII
	Invoke-Control @('daemon-reload') | Out-Null
	Invoke-Control @('start', 'r1-reload.service') | Out-Null
	$after = Invoke-Control @('status', 'r1-reload.service')
	if ($after.Contains("InvocationID=$invocation")) { throw 'Recreated unit reused old invocation' }
	Invoke-Control @('stop', 'r1-reload.service') | Out-Null
	$passed += 'reload-deletion-status-logs-stop-recreation'

	@'
& C:\winunitd-lab\winunit-notify.exe --ready
if ($LASTEXITCODE) { exit $LASTEXITCODE }
while ($true) { Start-Sleep 1 }
'@ | Set-Content "$root\r1-notify.ps1" -Encoding ASCII
	Write-FixtureUnit 'r1-notify.service' 'notify' 'r1-notify.ps1'
	Invoke-Control @('daemon-reload') | Out-Null
	for ($i = 0; $i -lt 10; $i++) {
		Invoke-Control @('start', 'r1-notify.service') | Out-Null
		Invoke-Control @('status', 'r1-notify.service') | Out-Null
		Invoke-Control @('stop', 'r1-notify.service') | Out-Null
	}
	$passed += 'ten-notify-ready-stop-reopen-cycles'
} finally {
	Stop-Service winunitd
	$survivors = @(Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -match '-File C:\\winunitd-lab\\r1-(output|reload|notify)\.ps1' })
	Stop-Transcript
	if ($survivors.Count) { throw 'Runtime fixture survived SCM stop' }
}
$windowsVersion = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
@{
	schema = 1
	commit = $manifest.commit
	fixture_sha256 = (Get-FileHash $PSCommandPath).Hash.ToLowerInvariant()
	build = "$($windowsVersion.CurrentBuildNumber).$($windowsVersion.UBR)"
	identity = 'SYSTEM'
	passed = $passed
	completed = [DateTime]::UtcNow.ToString('o')
} | ConvertTo-Json | Set-Content "$root\runtime-checks-result.json" -Encoding UTF8
