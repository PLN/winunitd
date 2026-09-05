$ErrorActionPreference = 'Stop'
$base = $PSScriptRoot
Start-Transcript (Join-Path $base 'post-setup.log') -Append
try {
	$deadline = (Get-Date).AddMinutes(20)
	while ((Get-ItemProperty 'HKLM:\SYSTEM\Setup').SystemSetupInProgress -ne 0) {
		if ((Get-Date) -ge $deadline) { throw 'Windows Setup did not finish before deadline' }
		Start-Sleep -Seconds 5
	}
	$p = Start-Process msiexec.exe -WindowStyle Hidden -PassThru -ArgumentList @('/i', "`"$base\qemu-ga-x86_64.msi`"", '/qn', '/norestart', '/L*v', "`"$base\guest-agent-install.log`"")
	if (-not $p.WaitForExit(300000)) { throw 'Guest-agent installation timed out' }
	$p.Refresh()
	if ($p.ExitCode -notin @(0, 3010)) { throw "Guest-agent installation failed: $($p.ExitCode)" }
	Set-Service QEMU-GA -StartupType Automatic
	Start-Service QEMU-GA
	# Remove cached answer files; provisioning media is detached by the controller.
	foreach ($relative in @('Panther\unattend.xml', 'Panther\Unattend\unattend.xml', 'System32\Sysprep\unattend.xml')) {
		$answer = Join-Path $env:SystemRoot $relative
		if (Test-Path -LiteralPath $answer) { Remove-Item -LiteralPath $answer -Force }
	}
	$os = Get-CimInstance Win32_OperatingSystem
	$version = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
	[ordered]@{schema=1; ready=$true; os_caption=$os.Caption; os_build="$($version.CurrentBuild).$($version.UBR)"; edition=$version.EditionID; secure_boot=(Confirm-SecureBootUEFI); guest_agent=(Get-Item "$env:ProgramFiles\qemu-ga\qemu-ga.exe").VersionInfo.FileVersion} | ConvertTo-Json | Set-Content (Join-Path $base 'ready.json')
	Unregister-ScheduledTask -TaskName 'winunitd-lab-post-setup' -Confirm:$false
} catch {
	Write-Error -ErrorRecord $_ -ErrorAction Continue
	exit 1
} finally { Stop-Transcript }
