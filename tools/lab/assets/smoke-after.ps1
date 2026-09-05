$ErrorActionPreference = 'Stop'
Set-Location C:\winunitd-lab
Start-Transcript C:\winunitd-lab\smoke-after-reboot.log
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'Expected SYSTEM' }
$oldBoot = Get-Content .\boot-before-reboot.txt
$newBoot = (Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToString('o')
if ($oldBoot -eq $newBoot) { throw 'No reboot observed' }
$deadline = (Get-Date).AddSeconds(180)
do {
	$unitStatus = & .\winctl.exe status smoke.service
	if ($LASTEXITCODE -eq 0) { break }
	Start-Sleep 3
} while ((Get-Date) -lt $deadline)
if ($LASTEXITCODE) { throw 'Enabled fixture did not recover after reboot' }
$unitStatus
$oldStatus = Get-Content .\status-before-reboot.txt -Raw
$oldInvocation = [regex]::Match($oldStatus, 'InvocationID=(\S+)').Groups[1].Value
$newInvocation = [regex]::Match(($unitStatus -join "`n"), 'InvocationID=(\S+)').Groups[1].Value
if (!$oldInvocation -or !$newInvocation -or $oldInvocation -eq $newInvocation) { throw 'Expected a new unit invocation' }
$fixture = Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*-File C:\winunitd-lab\fixture.ps1*' }
if (@($fixture).Count -ne 1) { throw 'Expected exactly one fixture process' }
if ($fixture.SessionId -ne 0) { throw 'Expected fixture in SYSTEM session 0' }
$fixture | Select-Object ProcessId, SessionId, CommandLine
& .\winctl.exe logs smoke.service
Get-CimInstance Win32_Service -Filter "Name='winunitd'" | Select-Object Name, State, StartName, StartMode
"Old boot: $oldBoot; new boot: $newBoot"
'PASS after reboot' | Set-Content .\smoke-after-reboot.result
Stop-Service winunitd
if (Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*-File C:\winunitd-lab\fixture.ps1*' }) { throw 'Fixture survived SCM stop' }
Stop-Transcript
