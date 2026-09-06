param([switch]$DisposableLab, [ValidateSet('BeforeReboot','AfterReboot')][string]$Phase = 'BeforeReboot')
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = 'C:\winunitd-beta-lab'
$installed = 'C:\Program Files\winunitd'
$data = 'C:\ProgramData\winunitd'
if (!$DisposableLab -or !(Test-Path "$root\disposable")) { throw 'Prepared disposable guest required' }
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'SYSTEM required' }
if (Get-NetRoute -DestinationPrefix @('0.0.0.0/0','::/0') -ErrorAction SilentlyContinue) { throw 'Disconnect maintenance routing' }
$manifests = @{}
foreach ($v in @('0.2.0','0.2.1')) {
	$m = Get-Content "$root\$v\package-manifest.json" -Raw | ConvertFrom-Json
	if ($m.dirty -or $m.payload.dirty -or $m.version -ne $v -or $m.commit -ne $m.payload.commit) { throw 'Clean matching package identity required' }
	$file = Get-Item "$root\$v\$($m.name)"
	if ($file.Length -ne $m.size -or (Get-FileHash $file.FullName).Hash -ne $m.sha256) { throw 'Package hash mismatch' }
	$manifests[$v] = $m
}
if ($manifests['0.2.0'].commit -ne $manifests['0.2.1'].commit) { throw 'Qualification pair source mismatch' }
function Control([string]$Arguments, [int]$Expected = 0) {
	$info = New-Object Diagnostics.ProcessStartInfo
	$info.FileName = "$installed\winctl.exe"
	$info.Arguments = $Arguments
	$info.UseShellExecute = $false
	$info.CreateNoWindow = $true
	$info.RedirectStandardOutput = $true
	$info.RedirectStandardError = $true
	$p = [Diagnostics.Process]::Start($info)
	try {
		$out = $p.StandardOutput.ReadToEndAsync(); $err = $p.StandardError.ReadToEndAsync()
		if (!$p.WaitForExit(30000)) { $p.Kill(); throw 'Control timeout' }
		$text = $out.GetAwaiter().GetResult() + $err.GetAwaiter().GetResult()
		if ($p.ExitCode -ne $Expected) { throw "Control failed: $Arguments : $text" }
		return $text
	} finally { $p.Dispose() }
}
function Ready {
	$deadline = [datetime]::UtcNow.AddSeconds(45)
	do {
		try { Control 'list-units' | Out-Null; return } catch {
			if ([datetime]::UtcNow -ge $deadline) { throw }; Start-Sleep 1
		}
	} while ($true)
}
function Stop-Manager {
	Stop-Service winunitd
	(Get-Service winunitd).WaitForStatus('Stopped', [timespan]::FromSeconds(180))
}
function Msi([string]$Name, [string]$Verb, [string]$Version, [string]$Extra = '', [int]$Expected = 0) {
	$package = "$root\$Version\$($manifests[$Version].name)"
	$p = Start-Process msiexec.exe -ArgumentList "$Verb `"$package`" /qn /norestart /l*v `"$root\$Name.log`" $Extra" -PassThru -WindowStyle Hidden
	if (!$p.WaitForExit(300000)) { throw 'MSI exceeded qualification deadline; inspect guest before proceeding' }
	if ($p.ExitCode -ne $Expected) { throw "MSI $Name returned $($p.ExitCode), expected $Expected" }
	Write-Output "PASS MSI $Name"
}
function Verify-Payload([string]$Version) {
	foreach ($a in $manifests[$Version].payload.artifacts) {
		if ((Get-FileHash "$installed\$($a.name)").Hash -ne $a.sha256) { throw 'Installed payload mismatch' }
	}
	if ((Control '--version').Trim() -ne "winctl $Version-beta") { throw 'Installed version mismatch' }
}
function Invocation {
	return [regex]::Match((Control 'status worker.service'), '(?m)^InvocationID=(\S+)').Groups[1].Value
}
function No-Worker {
	$children = @(Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'powershell.exe' -and $_.CommandLine -like '*while ($true)*worker is running*' })
	if ($children.Count) { throw 'Worker survived manager stop' }
}
Start-Transcript "$root\$Phase.log"
try {
	if ($Phase -eq 'BeforeReboot') {
		if ((Get-Service winunitd -ErrorAction SilentlyContinue) -or (Test-Path $installed) -or (Test-Path $data)) { throw 'Fresh product namespace required' }
		Msi 'install' '/i' '0.2.0'
		Ready; Verify-Payload '0.2.0'
		Copy-Item "$installed\examples\worker.service" "$data\units\worker.service"
		Copy-Item "$installed\examples\worker.target" "$data\units\worker.target"
		Control 'daemon-reload' | Out-Null
		Control 'enable worker.service' | Out-Null
		Control 'start worker.target' | Out-Null
		$first = Invocation
		Control 'restart worker.target' | Out-Null
		if (!$first -or (Invocation) -eq $first) { throw 'Group restart did not replace invocation' }
		$status = Control 'status worker.service'
		$workerPID = [int][regex]::Match($status, 'Main PID:\s+(\d+)').Groups[1].Value
		$beforeCrash = Invocation
		Stop-Process -Id $workerPID -Force
		$deadline = [datetime]::UtcNow.AddSeconds(30)
		do {
			Start-Sleep 1
			try { $afterCrash = Invocation } catch { $afterCrash = '' }
			if ([datetime]::UtcNow -ge $deadline) { throw 'Workload recovery timeout' }
		} while (!$afterCrash -or $afterCrash -eq $beforeCrash)
		$deadline = [datetime]::UtcNow.AddSeconds(30)
		while ((Control 'logs worker.service') -notmatch 'worker is running') {
			if ([datetime]::UtcNow -ge $deadline) { throw 'Worker output missing' }
			Start-Sleep 1
		}
		$unitHash = (Get-FileHash "$data\units\worker.service").Hash
		Msi 'running-upgrade-rejected' '/i' '0.2.1' '' 1603
		Verify-Payload '0.2.0'
		Stop-Manager; No-Worker
		Msi 'ordinary-repair' '/fa' '0.2.0'
		Ready; Verify-Payload '0.2.0'
		Stop-Manager
		Msi 'failed-upgrade' '/i' '0.2.1' 'WINUNITD_TEST_FAIL=1' 1603
		Verify-Payload '0.2.0'
		if ((Get-Service winunitd).Status -eq 'Stopped') { Start-Service winunitd }
		Ready
		Stop-Manager
		Msi 'upgrade' '/i' '0.2.1'
		Ready; Verify-Payload '0.2.1'
		if ((Get-FileHash "$data\units\worker.service").Hash -ne $unitHash) { throw 'Upgrade changed configuration' }
		Control 'start worker.service' | Out-Null
		@{invocation=(Invocation); boot=(Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToUniversalTime().ToString('o'); unit_hash=$unitHash} |
			ConvertTo-Json | Set-Content "$root\before-reboot.json" -Encoding UTF8
		Write-Output 'PASS pre-reboot qualification; reboot the guest, then run AfterReboot.'
	} else {
		$before = Get-Content "$root\before-reboot.json" -Raw | ConvertFrom-Json
		if ((Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToUniversalTime().ToString('o') -eq $before.boot) { throw 'Guest was not rebooted' }
		Ready; Verify-Payload '0.2.1'
		if ((Invocation) -eq $before.invocation) { throw 'Invocation did not change on reboot' }
		Stop-Manager; No-Worker
		Msi 'uninstall' '/x' '0.2.1'
		if ((Get-Service winunitd -ErrorAction SilentlyContinue) -or (Test-Path "$installed\winunitd.exe")) { throw 'Uninstall left service or binary' }
		if ((Get-FileHash "$data\units\worker.service").Hash -ne $before.unit_hash) { throw 'Uninstall lost configuration' }
		if (!(Get-ChildItem "$data\journal" -File)) { throw 'Uninstall lost journal' }
		Msi 'reinstall' '/i' '0.2.1'
		Ready; Verify-Payload '0.2.1'
		if (!(Invocation)) { throw 'Reinstall lost enablement' }
		Stop-Manager; No-Worker
		Msi 'final-uninstall' '/x' '0.2.1'
		$os = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
		@{schema=1; commit=$manifests['0.2.1'].commit; build="$($os.CurrentBuildNumber).$($os.UBR)"; identity='SYSTEM'; fixture_sha256=(Get-FileHash $PSCommandPath).Hash.ToLowerInvariant(); completed=[datetime]::UtcNow.ToString('o'); passed=@('install','running-upgrade-rejected','repair','failed-upgrade-restores-payload','upgrade-preserves-config','group-restart','workload-recovery','logs','reboot-enablement','uninstall-retains-data','reinstall','final-cleanup')} |
			ConvertTo-Json | Set-Content "$root\result.json" -Encoding UTF8
		Write-Output 'PASS product MSI qualification'
	}
} finally { Stop-Transcript }
