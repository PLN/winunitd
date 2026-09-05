$ErrorActionPreference = 'Stop'
$base = Join-Path $env:ProgramData 'winunitd-lab-bootstrap'
New-Item -ItemType Directory -Force $base | Out-Null
& icacls.exe $base /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Cannot protect bootstrap directory' }
Copy-Item "$PSScriptRoot\post-setup.ps1" $base
Copy-Item "$PSScriptRoot\qemu-ga-x86_64.msi" $base
Start-Transcript (Join-Path $base 'specialize.log')
try {
	& pnputil.exe /add-driver "$PSScriptRoot\vioser.inf" /install
	if ($LASTEXITCODE -notin @(0, 3010)) { throw 'VirtIO serial driver installation failed' }
	$action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$base\post-setup.ps1`""
	$trigger = New-ScheduledTaskTrigger -AtStartup
	$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
	$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Minutes 30) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
	Register-ScheduledTask -TaskName 'winunitd-lab-post-setup' -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
	# The independent task waits for Setup to finish; specialize must return now.
	Start-ScheduledTask -TaskName 'winunitd-lab-post-setup'
} finally { Stop-Transcript }
