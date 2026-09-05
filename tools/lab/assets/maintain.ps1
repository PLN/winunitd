# Supervised baseline preparation only; disconnect maintenance egress before tests.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'Expected SYSTEM' }
$base = Join-Path $env:ProgramData 'winunitd-lab-bootstrap'
if (-not (Test-Path (Join-Path $base 'ready.json'))) { throw 'Completed lab setup required' }
Start-Transcript (Join-Path $base 'maintenance.log') -Append
try {
	# Collect the previous wave before invoking this script again.
	foreach ($name in 'maintenance-result.json', 'updates-selected.json', 'updates-installed.json') {
		$path = Join-Path $base $name
		if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path }
	}
	'activation' | Set-Content (Join-Path $base 'maintenance-phase.txt')
	& cscript.exe //Nologo "$env:SystemRoot\System32\slmgr.vbs" /ato | Set-Content (Join-Path $base 'activation.log')
	if ($LASTEXITCODE) { throw 'Evaluation activation command failed' }
	$license = @(Get-CimInstance SoftwareLicensingProduct -Filter "ApplicationID='55c92734-d682-4d71-983e-d6ec3f16059f'" | Where-Object { $_.PartialProductKey -and $_.LicenseStatus -eq 1 })
	if ($license.Count -ne 1) { throw 'Expected one activated Windows evaluation license' }
	$license | Select-Object Name, LicenseStatus, GracePeriodRemaining, EvaluationEndDate | ConvertTo-Json | Set-Content (Join-Path $base 'license.json')
	'searching' | Set-Content (Join-Path $base 'maintenance-phase.txt')
	$session = New-Object -ComObject Microsoft.Update.Session
	$session.ClientApplicationID = 'winunitd qualification baseline'
	$searcher = $session.CreateUpdateSearcher()
	$searcher.ServerSelection = 2 # Windows Update, not a configured intranet server.
	$search = $searcher.Search("IsInstalled=0 and IsHidden=0 and Type='Software' and BrowseOnly=0")
	if ($search.ResultCode -ne 2) { throw 'Windows Update search was not completely successful' }
	$updates = New-Object -ComObject Microsoft.Update.UpdateColl
	$selected = @()
	foreach ($update in $search.Updates) {
		# Keep the chosen OS release; feature upgrades require a new baseline recipe.
		if (@($update.Categories | Where-Object CategoryID -eq '3689bdc8-b205-4af4-8d4a-a63924c5e9d5').Count) { continue }
		if ($update.InstallationBehavior.CanRequestUserInput) { throw 'Update requires interactive handling' }
		if (-not $update.EulaAccepted) { $update.AcceptEula() }
		$updates.Add($update) | Out-Null
		$selected += [ordered]@{title=$update.Title; id=$update.Identity.UpdateID; revision=$update.Identity.RevisionNumber; kb=@($update.KBArticleIDs)}
	}
	ConvertTo-Json -InputObject $selected -Depth 5 | Set-Content (Join-Path $base 'updates-selected.json')
	$systemInfo = New-Object -ComObject Microsoft.Update.SystemInfo
	$reboot = [bool]$systemInfo.RebootRequired
	if ($updates.Count) {
		'downloading' | Set-Content (Join-Path $base 'maintenance-phase.txt')
		$downloader = $session.CreateUpdateDownloader()
		$downloader.Updates = $updates
		$download = $downloader.Download()
		if ($download.ResultCode -ne 2) { throw 'Windows Update download was not completely successful' }
		'installing' | Set-Content (Join-Path $base 'maintenance-phase.txt')
		$installer = $session.CreateUpdateInstaller()
		$installer.Updates = $updates
		$installer.AllowSourcePrompts = $false
		$installer.ForceQuiet = $true
		if ($installer.RebootRequiredBeforeInstallation) { throw 'Reboot required before update installation' }
		$installed = $installer.Install()
		$details = for ($i=0; $i -lt $updates.Count; $i++) { $result=$installed.GetUpdateResult($i); [ordered]@{id=$updates.Item($i).Identity.UpdateID; code=$result.ResultCode; hresult=$result.HResult; reboot=$result.RebootRequired} }
		ConvertTo-Json -InputObject @($details) -Depth 4 | Set-Content (Join-Path $base 'updates-installed.json')
		if ($installed.ResultCode -ne 2) { throw 'Windows Update installation was not completely successful' }
		$reboot = $reboot -or $installed.RebootRequired
	}
	[ordered]@{schema=1; completed=(Get-Date).ToUniversalTime().ToString('o'); selected=$updates.Count; reboot_required=$reboot} | ConvertTo-Json | Set-Content (Join-Path $base 'maintenance-result.json')
	'completed' | Set-Content (Join-Path $base 'maintenance-phase.txt')
} catch {
	'failed' | Set-Content (Join-Path $base 'maintenance-phase.txt')
	Write-Error -ErrorRecord $_ -ErrorAction Continue
	exit 1
} finally { Stop-Transcript }
