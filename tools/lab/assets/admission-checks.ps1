param([switch]$DisposableLab)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (!$DisposableLab) { throw 'Explicit disposable-lab acknowledgement required' }
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'Expected SYSTEM' }
if (Get-NetRoute -DestinationPrefix @('0.0.0.0/0', '::/0') -ErrorAction SilentlyContinue) { throw 'Disconnect maintenance routing' }
$root = 'C:\winunitd-lab'
$service = Get-CimInstance Win32_Service -Filter "Name='winunitd'"
if (!$service -or $service.PathName -notmatch '^"?C:\\winunitd-lab\\winunitd\.exe"?\s') { throw 'Expected isolated lab service' }
$manifest = Get-Content "$root\build-manifest.json" -Raw | ConvertFrom-Json
foreach ($artifact in $manifest.artifacts) {
	if ((Get-FileHash (Join-Path $root $artifact.name)).Hash -ne $artifact.sha256) { throw 'Artifact hash mismatch' }
}
if (Test-Path -LiteralPath "$root\admission") { throw 'Fresh admission fixture required' }
foreach ($i in 0..32) {
	if (Test-Path -LiteralPath "$root\data\units\admission-$i.service") { throw 'Fixture unit already exists' }
}
function Start-Control {
	param([string]$Arguments)
	$info = New-Object Diagnostics.ProcessStartInfo
	$info.FileName = "$root\winctl.exe"
	$info.Arguments = $Arguments
	$info.UseShellExecute = $false
	$info.CreateNoWindow = $true
	$info.RedirectStandardOutput = $true
	$info.RedirectStandardError = $true
	$p = [Diagnostics.Process]::Start($info)
	return @{ Process = $p; Output = $p.StandardOutput.ReadToEndAsync(); Errors = $p.StandardError.ReadToEndAsync() }
}
function Complete-Control {
	param($Client, [int]$Timeout = 30000)
	$p = $Client.Process
	if (!$p.WaitForExit($Timeout)) { throw 'Control request exceeded fixture deadline' }
	return @{ Code = $p.ExitCode; Text = $Client.Output.GetAwaiter().GetResult() + $Client.Errors.GetAwaiter().GetResult() }
}
function Invoke-Control {
	param([string]$Arguments)
	$c = Start-Control $Arguments
	try { return Complete-Control $c } finally {
		if (!$c.Process.HasExited) { $c.Process.Kill() }
		$c.Process.Dispose()
	}
}
function Assert-Control {
	param([string]$Arguments)
	$r = Invoke-Control $Arguments
	if ($r.Code -ne 0) { throw "Control failed: $Arguments : $($r.Text)" }
}
$clients = @()
New-Item -ItemType Directory "$root\admission" | Out-Null
@'
param([int]$Index)
Set-Content -LiteralPath "C:\winunitd-lab\admission\ready-$Index" -Value 'ready'
while (!(Test-Path -LiteralPath 'C:\winunitd-lab\admission\release')) { Start-Sleep -Milliseconds 100 }
'@ | Set-Content "$root\admission\hold.ps1" -Encoding ASCII
foreach ($i in 0..32) {
	@"
[Service]
Type=oneshot
ExecStart=C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe
ExecStartArg=-NoProfile
ExecStartArg=-ExecutionPolicy
ExecStartArg=Bypass
ExecStartArg=-File
ExecStartArg=$root\admission\hold.ps1
ExecStartArg=$i
TimeoutStartSec=180s
TimeoutStopSec=10s
"@ | Set-Content "$root\data\units\admission-$i.service" -Encoding ASCII
}
Start-Transcript "$root\admission-checks.log"
try {
	Start-Service winunitd
	Assert-Control 'daemon-reload'
	foreach ($i in 0..31) { $clients += Start-Control "start admission-$i.service" }
	$deadline = [DateTime]::UtcNow.AddSeconds(90)
	do {
		if (@($clients | Where-Object { $_.Process.HasExited }).Count) { throw 'Start completed before admission was saturated' }
		$ready = @(Get-ChildItem -LiteralPath "$root\admission" -Filter 'ready-*').Count
		if ($ready -eq 32) { break }
		if ([DateTime]::UtcNow -ge $deadline) { throw 'Did not admit 32 simultaneous starts' }
		Start-Sleep -Milliseconds 200
	} while ($true)
	$r = Invoke-Control 'start admission-32.service'
	if ($r.Code -eq 0 -or $r.Text -notmatch 'start transaction capacity exhausted') { throw 'Expected explicit overload rejection' }
	if (Test-Path -LiteralPath "$root\admission\ready-32") { throw 'Rejected start launched a child' }
	Assert-Control 'stop admission-0.service'
	$r = Complete-Control $clients[0]
	if ($r.Code -eq 0) { throw 'Stopped pending start reported success' }
	# A fresh real start must acquire the released slot while the other 31 remain pending.
	$replacement = Start-Control 'start admission-32.service'
	$clients += $replacement
	$deadline = [DateTime]::UtcNow.AddSeconds(30)
	while (!(Test-Path -LiteralPath "$root\admission\ready-32")) {
		if ($replacement.Process.HasExited -or [DateTime]::UtcNow -ge $deadline) { throw 'Released admission slot was not reusable' }
		Start-Sleep -Milliseconds 100
	}
	Set-Content -LiteralPath "$root\admission\release" -Value 'release'
	foreach ($c in $clients[1..32]) {
		$r = Complete-Control $c
		if ($r.Code -ne 0) { throw "Accepted start failed: $($r.Text)" }
	}
} finally {
	try {
		Stop-Service winunitd
		if (Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*-File C:\winunitd-lab\admission\hold.ps1*' }) { throw 'Fixture child survived SCM stop' }
	} finally {
		foreach ($c in $clients) {
			if (!$c.Process.HasExited -and !$c.Process.WaitForExit(5000)) { $c.Process.Kill() }
			$c.Process.Dispose()
		}
		Stop-Transcript
	}
}
$os = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
@{
	schema = 1
	commit = $manifest.commit
	fixture_sha256 = (Get-FileHash $PSCommandPath).Hash.ToLowerInvariant()
	build = "$($os.CurrentBuildNumber).$($os.UBR)"
	identity = 'SYSTEM'
	passed = @('32-pending-starts', 'overload-rejected-before-launch', 'stop-while-full', 'released-slot-reused', 'accepted-completions-drained', 'scm-stop-no-survivors')
	completed = [DateTime]::UtcNow.ToString('o')
} | ConvertTo-Json | Set-Content "$root\admission-checks-result.json" -Encoding UTF8
