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
foreach ($name in @('ops.target','ops-active.service','ops-idle.service','ops-failed.service')) {
	if (Test-Path -LiteralPath "$root\data\units\$name") { throw 'Fresh operation namespace required' }
}
function Invoke-Control {
	param([string]$Arguments, [int[]]$Expected = @(0))
	$info = New-Object Diagnostics.ProcessStartInfo
	$info.FileName = "$root\winctl.exe"
	$info.Arguments = $Arguments
	$info.UseShellExecute = $false
	$info.CreateNoWindow = $true
	$info.RedirectStandardOutput = $true
	$info.RedirectStandardError = $true
	$p = [Diagnostics.Process]::Start($info)
	try {
		$output = $p.StandardOutput.ReadToEndAsync()
		$errors = $p.StandardError.ReadToEndAsync()
		if (!$p.WaitForExit(30000)) { $p.Kill(); throw 'Control exceeded fixture deadline' }
		$text = $output.GetAwaiter().GetResult() + $errors.GetAwaiter().GetResult()
		if ($p.ExitCode -notin $Expected) { throw "Control failed: $Arguments : $text" }
		return $text
	} finally { $p.Dispose() }
}
function Field {
	param([string]$Text, [string]$Name)
	return [regex]::Match($Text, ('(?m)^'+[regex]::Escape($Name)+'=(\S+)')).Groups[1].Value
}
function Start-LabService {
	Start-Service winunitd
	$deadline = [DateTime]::UtcNow.AddSeconds(45)
	do {
		try { Invoke-Control 'list-units' | Out-Null; break } catch {
			if ([DateTime]::UtcNow -ge $deadline) { throw }; Start-Sleep 1
		}
	} while ($true)
}
@'
while ($true) { Write-Output 'operation fixture'; Start-Sleep 1 }
'@ | Set-Content "$root\operations-fixture.ps1" -Encoding ASCII
"[Unit]`nDescription=Operation fixture" | Set-Content "$root\data\units\ops.target" -Encoding ASCII
foreach ($name in @('ops-active.service','ops-idle.service')) {
	@"
[Unit]
PartOf=ops.target
After=ops.target
[Service]
Type=simple
ExecStart=C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe
ExecStartArg=-NoProfile
ExecStartArg=-ExecutionPolicy
ExecStartArg=Bypass
ExecStartArg=-File
ExecStartArg=$root\operations-fixture.ps1
TimeoutStopSec=10s
"@ | Set-Content "$root\data\units\$name" -Encoding ASCII
}
"[Service]`nExecStart=C:\winunitd-lab\absent-operation-fixture.exe" | Set-Content "$root\data\units\ops-failed.service" -Encoding ASCII
Start-Transcript "$root\operations-checks.log"
try {
	Start-LabService
	Invoke-Control 'daemon-reload' | Out-Null
	Invoke-Control 'start ops.target' | Out-Null
	Invoke-Control 'start ops-active.service' | Out-Null
	$oldInvocation = Field (Invoke-Control 'status ops-active.service') 'InvocationID'
	$restartID = Field (Invoke-Control 'restart ops.target') 'OperationID'
	$active = Invoke-Control 'status ops-active.service'
	$newInvocation = Field $active 'InvocationID'
	if (!$oldInvocation -or !$newInvocation -or $newInvocation -eq $oldInvocation) { throw 'Active PartOf member did not restart' }
	$idle = Invoke-Control 'status ops-idle.service' @(3)
	if ($idle -notmatch 'Active: inactive') { throw 'Restart activated an idle PartOf member' }
	if (!$restartID -or (Field $active 'LastOperationID') -ne $restartID) { throw 'Restart participation identity missing' }
	$restart = Invoke-Control "operation $restartID"
	if ((Field $restart 'State') -ne 'succeeded' -or (Field $restart 'Action') -ne 'restart') { throw 'Successful restart outcome missing' }
	$failedID = Field (Invoke-Control 'start ops-failed.service' @(1)) 'OperationID'
	if (!$failedID) { throw 'Admitted failure omitted operation ID' }
	$stopID = Field (Invoke-Control 'stop ops-failed.service') 'OperationID'
	if (!$stopID -or $stopID -eq $failedID) { throw 'Stop operation did not get independent identity' }
	Remove-Item -LiteralPath "$root\data\units\ops-failed.service"
	Invoke-Control 'daemon-reload' | Out-Null
	if ((Field (Invoke-Control "operation $failedID" @(1)) 'State') -ne 'failed') { throw 'Removal or retry erased failed outcome' }
	if ((Field (Invoke-Control "operation $stopID") 'State') -ne 'succeeded') { throw 'Later stop outcome missing' }
	Stop-Service winunitd
	Start-LabService
	Invoke-Control "operation $restartID" @(4) | Out-Null
} finally {
	Stop-Service winunitd
	$survivors = @(Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*-File C:\winunitd-lab\operations-fixture.ps1*' })
	Stop-Transcript
	if ($survivors.Count) { throw 'Operation fixture survived SCM stop' }
}
$os = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
@{
	schema = 1
	commit = $manifest.commit
	fixture_sha256 = (Get-FileHash $PSCommandPath).Hash.ToLowerInvariant()
	build = "$($os.CurrentBuildNumber).$($os.UBR)"
	identity = 'SYSTEM'
	passed = @('active-partof-restored', 'inactive-partof-stays-inactive', 'successful-restart-query', 'failed-operation-survives-removal', 'later-stop-keeps-independent-outcome', 'manager-restart-expires-history', 'scm-stop-no-survivors')
	completed = [DateTime]::UtcNow.ToString('o')
} | ConvertTo-Json | Set-Content "$root\operations-checks-result.json" -Encoding UTF8
