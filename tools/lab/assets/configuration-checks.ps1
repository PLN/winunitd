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
foreach ($name in @('config.service','config-extra.service','config-invalid.service')) {
	if (Test-Path -LiteralPath "$root\data\units\$name") { throw 'Fresh configuration namespace required' }
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
function Write-Configuration {
	param([string]$Label)
	@"
[Unit]
Description=Configuration fixture $Label
[Service]
Type=simple
ExecStart=C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe
ExecStartArg=-NoProfile
ExecStartArg=-ExecutionPolicy
ExecStartArg=Bypass
ExecStartArg=-File
ExecStartArg=$root\configuration-fixture.ps1
ExecStartArg=$Label
TimeoutStopSec=10s
"@ | Set-Content "$root\data\units\config.service" -Encoding ASCII
}
@'
param([string]$Label)
while ($true) { Write-Output "configuration $Label"; Start-Sleep 1 }
'@ | Set-Content "$root\configuration-fixture.ps1" -Encoding ASCII
Write-Configuration 'old'
Start-Transcript "$root\configuration-checks.log"
try {
	Start-Service winunitd
	$deadline = [DateTime]::UtcNow.AddSeconds(45)
	do {
		try { Invoke-Control 'list-units' | Out-Null; break } catch {
			if ([DateTime]::UtcNow -ge $deadline) { throw }; Start-Sleep 1
		}
	} while ($true)
	Invoke-Control 'daemon-reload' | Out-Null
	Invoke-Control 'start config.service' | Out-Null
	$before = Invoke-Control 'status config.service'
	$oldRevision = Field $before 'ConfigRevision'
	$oldInvocation = Field $before 'InvocationID'
	if (!$oldRevision -or !$oldInvocation -or (Field $before 'InvocationConfigRevision') -ne $oldRevision) { throw 'Missing captured configuration identity' }
	Write-Configuration 'new'
	Copy-Item -LiteralPath "$root\data\units\config.service" -Destination "$root\data\units\config-extra.service"
	"[Service]`nType=invalid" | Set-Content "$root\data\units\config-invalid.service" -Encoding ASCII
	Invoke-Control 'daemon-reload' @(1) | Out-Null
	$rejected = Invoke-Control 'status config.service'
	if ((Field $rejected 'ConfigRevision') -ne $oldRevision -or (Field $rejected 'InvocationID') -ne $oldInvocation -or $rejected -notmatch 'Configuration fixture old') { throw 'Invalid candidate was partially accepted' }
	Invoke-Control 'status config-extra.service' @(4) | Out-Null
	Remove-Item -LiteralPath "$root\data\units\config-invalid.service"
	Invoke-Control 'daemon-reload' | Out-Null
	$accepted = Invoke-Control 'status config.service'
	$newRevision = Field $accepted 'ConfigRevision'
	if (!$newRevision -or $newRevision -eq $oldRevision -or (Field $accepted 'InvocationConfigRevision') -ne $oldRevision -or (Field $accepted 'InvocationID') -ne $oldInvocation) { throw 'Valid reload replaced or relabelled the live invocation' }
	Invoke-Control 'stop config.service' | Out-Null
	Invoke-Control 'start config.service' | Out-Null
	$fresh = Invoke-Control 'status config.service'
	if ((Field $fresh 'InvocationConfigRevision') -ne $newRevision -or (Field $fresh 'InvocationID') -eq $oldInvocation) { throw 'Fresh start did not adopt accepted configuration' }
	Start-Sleep 2
	if ((Invoke-Control 'logs config.service') -notmatch 'configuration new') { throw 'New configuration did not execute' }
	Remove-Item -LiteralPath "$root\data\units\config.service"
	Invoke-Control 'daemon-reload' | Out-Null
	$removed = Invoke-Control 'status config.service'
	if ($removed -notmatch 'Loaded: unavailable' -or (Field $removed 'ConfigRevision') -ne '' -or (Field $removed 'InvocationConfigRevision') -ne $newRevision) { throw 'Removal lost captured configuration or stop routing' }
	Invoke-Control 'stop config.service' | Out-Null
	Invoke-Control 'start config.service' @(1) | Out-Null
} finally {
	Stop-Service winunitd
	$survivors = @(Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*-File C:\winunitd-lab\configuration-fixture.ps1*' })
	Stop-Transcript
	if ($survivors.Count) { throw 'Configuration fixture survived SCM stop' }
}
$os = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
@{
	schema = 1
	commit = $manifest.commit
	fixture_sha256 = (Get-FileHash $PSCommandPath).Hash.ToLowerInvariant()
	build = "$($os.CurrentBuildNumber).$($os.UBR)"
	identity = 'SYSTEM'
	passed = @('invalid-candidate-atomic', 'live-revision-retained', 'fresh-start-adopts-revision', 'removed-live-stop-access', 'scm-stop-no-survivors')
	completed = [DateTime]::UtcNow.ToString('o')
} | ConvertTo-Json | Set-Content "$root\configuration-checks-result.json" -Encoding UTF8
