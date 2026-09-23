#requires -Version 5.1
<#
.SYNOPSIS
  Disposable-guest acceptance harness for the product MSI.

.DESCRIPTION
  Runs one R6.1 / R6.4 / deferred R6.3 case and appends a redacted JSON
  line under the private evidence directory. Raw MSI logs stay there.
  The script does not assign an evidence id. Pass -EvidenceId only when
  publishing a reviewed run. A skipped case is missing evidence.

  Installer 0.1.0 is the only recorded product package. -OlderMsi is an
  operator-supplied earlier package with the same UpgradeCode, or a
  package discovered under dist\wix, the top of packaging\wix, or
  beside the product MSI. This script does not invent a version.
  -BetaMsi, when omitted, is discovered from the packaging\beta output
  layout (dist\beta, the top of packaging\beta, or beside the product
  MSI). The version is read from that file.

.EXAMPLE
  powershell -NoProfile -File tests/installer/acceptance.ps1 -Case list

.EXAMPLE
  powershell -NoProfile -File tests/installer/acceptance.ps1 -DisposableGuest -MsiPath <product-msi> -EvidenceDirectory <private-directory> -Case quiet-install

.EXAMPLE
  powershell -NoProfile -File tests/installer/acceptance.ps1 -DisposableGuest -MsiPath <product-msi> -EvidenceDirectory <private-directory> -Case gui-install
#>
[CmdletBinding()]
param(
	[string]$MsiPath,
	[string]$EvidenceDirectory,
	[Parameter(Mandatory)][string]$Case,
	[string]$OlderMsi,
	[string]$BetaMsi,
	[string]$EvidenceId,
	[string]$SourceCommit,
	[switch]$DisposableGuest
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0
$ProgressPreference = 'SilentlyContinue'

$InstallerVersion = '0.1.0'
$ProductCode = '78C43374-5AB7-4E81-B9CF-09E8ACD01133'
$UpgradeCode = 'A512B91F-1883-40FD-8EDB-5B8C5708DEEA'
$BetaUpgradeCode = '9443AE50-251B-4A46-9465-B835A3A27133'
$script:PackageSha = ''
$script:Commit = ''
$script:FixturesWritten = $false
$script:Guest = $null
$script:Identity = ''
$script:Offline = $null
$script:Interactive = $false
$script:SummaryIdentity = ''
$script:GuestSku = 'unclaimed'
$script:SecLogonPriorStart = $null
$script:SecLogonPriorRunning = $false

function ConvertTo-Array($Value) {
	if ($null -eq $Value) { return @() }
	return @($Value)
}

function ConvertTo-GuidText([string]$Text) {
	return $Text.Trim().Trim('{}').ToUpperInvariant()
}

function ConvertTo-MsiArg([string]$Text) {
	if ($Text -match '["\r\n]') { throw 'unsupported argument' }
	return '"' + $Text + '"'
}

function ConvertTo-PathKey([string]$Path) {
	return $Path.Trim().TrimEnd('\').ToLowerInvariant()
}

function ConvertTo-MsiVersion([string]$Text) {
	if (-not $Text) { return $null }
	$parts = $Text.Split('.')
	$nums = @(0, 0, 0)
	$limit = $parts.Length
	if ($limit -gt 3) { $limit = 3 }
	for ($i = 0; $i -lt $limit; $i++) {
		$n = 0
		if (-not [int]::TryParse($parts[$i], [ref]$n)) { return $null }
		$nums[$i] = $n
	}
	return [version]::new($nums[0], $nums[1], $nums[2])
}

function Test-VersionLess([string]$Left, [string]$Right) {
	$a = ConvertTo-MsiVersion $Left
	$b = ConvertTo-MsiVersion $Right
	if ($null -eq $a -or $null -eq $b) { return $false }
	return $a -lt $b
}

function Get-MsiProperty([string]$Path, [string]$Name) {
	if ($Name -notmatch '^[A-Za-z0-9_]+$') { throw 'unsupported MSI property name' }
	$installer = New-Object -ComObject WindowsInstaller.Installer
	$db = $installer.OpenDatabase($Path, 0)
	$sql = "SELECT ``Value`` FROM ``Property`` WHERE ``Property``='$Name'"
	$view = $db.OpenView($sql)
	$view.Execute() | Out-Null
	$rec = $view.Fetch()
	if (-not $rec) { return '' }
	$value = [string]$rec.StringData(1)
	$view.Close() | Out-Null
	return $value
}

function Get-ProductExe { Join-Path $env:ProgramFiles 'winunitd\bin\winunitd.exe' }
function Get-Winctl { Join-Path $env:ProgramFiles 'winunitd\bin\winctl.exe' }
function Get-DataRoot { Join-Path $env:ProgramData 'winunitd' }
function Get-BetaExe { Join-Path $env:ProgramFiles 'winunitd\winunitd.exe' }

function Test-ProductPresent {
	$code = '{' + $ProductCode + '}'
	$key = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\' + $code
	if (Test-Path -LiteralPath $key) { return $true }
	return Test-Path -LiteralPath (Get-ProductExe)
}

function Get-ServiceState {
	$svc = Get-Service -Name winunitd -ErrorAction SilentlyContinue
	if (-not $svc) { return 'absent' }
	switch ($svc.Status) {
		'Running' { return 'running' }
		'Stopped' { return 'stopped' }
		default { return 'other' }
	}
}

function Wait-ServiceState([string]$State, [int]$Seconds) {
	$deadline = (Get-Date).AddSeconds($Seconds)
	do {
		if ((Get-ServiceState) -eq $State) { return $true }
		if ((Get-Date) -ge $deadline) { return $false }
		Start-Sleep -Seconds 1
	} while ($true)
}

function Start-Winunitd {
	if ((Get-ServiceState) -eq 'running') { return $true }
	Start-Service -Name winunitd -ErrorAction SilentlyContinue
	return Wait-ServiceState 'running' 60
}

function Stop-Winunitd {
	if ((Get-ServiceState) -eq 'absent') { return $false }
	if ((Get-ServiceState) -eq 'stopped') { return $true }
	Stop-Service -Name winunitd -Force -ErrorAction SilentlyContinue
	return Wait-ServiceState 'stopped' 180
}

function Get-BootStamp {
	return (Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToUniversalTime().Ticks
}

function Get-PathSegments {
	$raw = [Environment]::GetEnvironmentVariable('Path', 'Machine')
	$items = New-Object System.Collections.Generic.List[string]
	if (-not $raw) { return $items.ToArray() }
	foreach ($part in $raw.Split(';')) {
		if ($part -ne '') { [void]$items.Add($part) }
	}
	return $items.ToArray()
}

function Test-PathOwned {
	$key = 'HKLM:\Software\PLN\winunitd'
	if (-not (Test-Path -LiteralPath $key)) { return $false }
	$prop = Get-ItemProperty -LiteralPath $key
	if ($prop.PSObject.Properties.Name -notcontains 'PathEntry') { return $false }
	$bin = Join-Path $env:ProgramFiles 'winunitd\bin'
	$want = ConvertTo-PathKey $bin
	if ((ConvertTo-PathKey ([string]$prop.PathEntry)) -ne $want) { return $false }
	foreach ($segment in @(Get-PathSegments)) {
		if ((ConvertTo-PathKey $segment) -eq $want) { return $true }
	}
	return $false
}

function Test-PathRemoval([string[]]$Before, [string[]]$After) {
	$binKey = ConvertTo-PathKey (Join-Path $env:ProgramFiles 'winunitd\bin')
	$afterKeys = New-Object System.Collections.Generic.List[string]
	foreach ($segment in $After) { [void]$afterKeys.Add((ConvertTo-PathKey $segment)) }
	foreach ($key in $afterKeys) {
		$seen = $false
		foreach ($old in $Before) {
			if ((ConvertTo-PathKey $old) -eq $key) { $seen = $true }
		}
		if (-not $seen) { return 'PATH gained an unexpected entry' }
	}
	foreach ($old in $Before) {
		$key = ConvertTo-PathKey $old
		if ($afterKeys.Contains($key)) { continue }
		if ($key -ne $binKey) { return 'PATH removed an unrelated entry' }
	}
	if ($afterKeys.Contains($binKey)) { return 'owned PATH entry remained' }
	$key = 'HKLM:\Software\PLN\winunitd'
	if (Test-Path -LiteralPath $key) {
		$prop = Get-ItemProperty -LiteralPath $key
		if ($prop.PSObject.Properties.Name -contains 'PathEntry' -and [string]$prop.PathEntry) {
			return 'owned PATH entry remained'
		}
	}
	return ''
}

function Get-EventSourceState {
	$key = 'HKLM:\SYSTEM\CurrentControlSet\Services\EventLog\Application\winunitd'
	if (-not (Test-Path -LiteralPath $key)) { return 'absent' }
	$prop = Get-ItemProperty -LiteralPath $key
	if ($prop.PSObject.Properties.Name -notcontains 'EventMessageFile') { return 'other' }
	$file = [string]$prop.EventMessageFile
	if ($file.TrimEnd('\').ToLowerInvariant().EndsWith('\winunitd\bin\winunitd.exe')) { return 'present' }
	return 'other'
}

function Test-Layout {
	$root = Join-Path $env:ProgramFiles 'winunitd'
	$bin = Join-Path $root 'bin'
	foreach ($name in @('winunitd.exe', 'winctl.exe', 'winunit-notify.exe')) {
		if (-not (Test-Path -LiteralPath (Join-Path $bin $name))) { return $false }
	}
	$doc = Join-Path $root 'doc'
	foreach ($name in @('LICENSE', 'THIRD-PARTY-NOTICES.txt', 'INSTALLATION.md', 'UNIT-REFERENCE.md')) {
		if (-not (Test-Path -LiteralPath (Join-Path $doc $name))) { return $false }
	}
	$examples = Join-Path $doc 'examples'
	foreach ($name in @('worker.service', 'worker.target', 'README.md')) {
		if (-not (Test-Path -LiteralPath (Join-Path $examples $name))) { return $false }
	}
	$data = Get-DataRoot
	foreach ($name in @('units', 'enabled', 'journal', 'runtime', 'linger', 'daemon')) {
		$dir = Join-Path $data $name
		if (-not (Test-Path -LiteralPath $dir)) { return $false }
		$item = Get-Item -LiteralPath $dir -Force
		if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { return $false }
	}
	if (Test-Path -LiteralPath (Join-Path $data 'units\worker.service')) { return $false }
	return $true
}

function Get-ServiceIdentityNote {
	$svc = Get-CimInstance Win32_Service -Filter "Name='winunitd'"
	if (-not $svc) { return 'service identity mismatch' }
	if ($svc.DisplayName -ne 'WinUnit Manager') { return 'service identity mismatch' }
	if ($svc.StartName -ne 'LocalSystem' -and $svc.StartName -ne 'NT AUTHORITY\SYSTEM') { return 'service identity mismatch' }
	if ($svc.StartMode -ne 'Auto') { return 'service identity mismatch' }
	$path = [string]$svc.PathName
	if ($path -notlike ('*' + (Get-ProductExe) + '*')) { return 'service identity mismatch' }
	if ($path -notlike '*--base-dir*') { return 'service identity mismatch' }
	if ($path -notlike ('*' + (Get-DataRoot) + '*')) { return 'service identity mismatch' }
	return ''
}

function Get-PayloadHashes {
	$bin = Join-Path $env:ProgramFiles 'winunitd\bin'
	$hashes = [ordered]@{}
	foreach ($name in @('winunitd.exe', 'winctl.exe', 'winunit-notify.exe')) {
		$path = Join-Path $bin $name
		if (Test-Path -LiteralPath $path) {
			$hashes[$name] = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
		}
	}
	return $hashes
}

function Test-PrerequisiteAction([string]$Log) {
	if (-not $Log -or -not (Test-Path -LiteralPath $Log)) { return $false }
	$text = [IO.File]::ReadAllText($Log)
	return [regex]::IsMatch($text, '(?i)(^|[\s\\/"])((go|dotnet|wix|python)\.exe)([\s\\/"]|$)')
}

function Get-LogMarkers([string]$Log) {
	$known = @(
		'preflight conflict:',
		'reparse point',
		'unmanaged winunitd binary path',
		'abort replacement',
		'A newer package is installed.',
		'The unsigned beta package is installed.',
		'InjectServiceFailure',
		'UILevel = 3'
	)
	$found = New-Object System.Collections.Generic.List[string]
	if (-not $Log -or -not (Test-Path -LiteralPath $Log)) { return $found.ToArray() }
	$text = [IO.File]::ReadAllText($Log)
	foreach ($item in $known) {
		if ($text.Contains($item)) { [void]$found.Add($item) }
	}
	return $found.ToArray()
}

function Get-InstallFailureNote($Run) {
	if ($Run.Reboot) { return 'reboot started' }
	if ($Run.ExitCode -ne 0) { return 'unexpected exit' }
	if (-not (Test-Layout)) { return 'layout mismatch' }
	$identity = Get-ServiceIdentityNote
	if ($identity) { return $identity }
	if ((Get-ServiceState) -ne 'running') { return 'service is not running' }
	if ((Get-EventSourceState) -ne 'present') { return 'event source mismatch' }
	if (-not (Test-PathOwned)) { return 'PATH ownership mismatch' }
	if (Test-PrerequisiteAction $Run.Log) { return 'destination prerequisite invoked' }
	return ''
}

function New-MsiexecStartInfo([string]$Arguments, [bool]$Shell, [bool]$Hidden, [string]$User, [System.Security.SecureString]$Password, [bool]$Profile) {
	$info = New-Object System.Diagnostics.ProcessStartInfo
	$info.FileName = Join-Path $env:SystemRoot 'System32\msiexec.exe'
	$info.Arguments = $Arguments
	$info.UseShellExecute = $Shell
	if ($Hidden) { $info.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Hidden }
	else { $info.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Normal }
	if ($User) {
		$info.UserName = $User
		$info.Domain = '.'
		$info.Password = $Password
		$info.LoadUserProfile = $Profile
		$info.WorkingDirectory = $env:SystemRoot
	}
	return $info
}

function Invoke-Msiexec {
	param(
		[Parameter(Mandatory)][string]$LogName,
		[Parameter(Mandatory)][string[]]$Words,
		[switch]$Quiet,
		[switch]$BasicUi,
		[switch]$TestFail,
		[int]$TimeoutSec = 360,
		[string]$RunAs,
		[System.Security.SecureString]$RunAsPassword
	)
	if ($Quiet -and $BasicUi) { throw 'unsupported UI level' }
	$log = Join-Path $EvidenceDirectory ($LogName + '.log')
	$argv = New-Object System.Collections.Generic.List[string]
	foreach ($word in $Words) { [void]$argv.Add($word) }
	if ($Quiet) { [void]$argv.Add('/qn') }
	elseif ($BasicUi) { [void]$argv.Add('/qb!') }
	[void]$argv.Add('/norestart')
	[void]$argv.Add('/L*v')
	[void]$argv.Add((ConvertTo-MsiArg $log))
	$previousFail = $env:WINUNITD_TEST_FAIL
	if ($TestFail) { $env:WINUNITD_TEST_FAIL = '1' }
	else { Remove-Item Env:WINUNITD_TEST_FAIL -ErrorAction SilentlyContinue }
	$boot = Get-BootStamp
	try {
		$arguments = ($argv -join ' ')
		$hidden = [bool]$Quiet -or [bool]$RunAs
		if ($RunAs) {
			$exit = Start-FixtureMsiexec -User $RunAs -Password $RunAsPassword -Arguments $arguments -TimeoutSec $TimeoutSec
			$reboot = (Get-BootStamp) -ne $boot
			return [pscustomobject]@{ ExitCode = [int]$exit; Log = $log; Reboot = [bool]$reboot }
		} else {
			$info = New-MsiexecStartInfo $arguments $true $hidden '' $null $false
			$proc = [System.Diagnostics.Process]::Start($info)
		}
		if (-not $proc.WaitForExit($TimeoutSec * 1000)) {
			try { $proc.Kill() } catch { }
			throw 'msiexec timed out'
		}
		$proc.Refresh()
		$reboot = (Get-BootStamp) -ne $boot
		return [pscustomobject]@{ ExitCode = [int]$proc.ExitCode; Log = $log; Reboot = [bool]$reboot }
	} finally {
		if ($null -eq $previousFail) { Remove-Item Env:WINUNITD_TEST_FAIL -ErrorAction SilentlyContinue }
		else { $env:WINUNITD_TEST_FAIL = $previousFail }
	}
}

function New-CaseResult {
	param(
		[string]$Status,
		[string]$Note,
		$Exit,
		[string]$Log,
		[bool]$Reboot,
		[string]$Before,
		[string]$After,
		$DataRetained
	)
	[pscustomobject]@{
		Status = $Status
		Note = $Note
		Exit = $Exit
		Log = $Log
		Reboot = $Reboot
		Before = $Before
		After = $After
		DataRetained = $DataRetained
	}
}

function Get-GuestFacts {
	$cv = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
	$product = [string]$cv.ProductName
	$edition = ''
	if ($cv.PSObject.Properties.Name -contains 'EditionID') { $edition = [string]$cv.EditionID }
	$buildNo = [string]$cv.CurrentBuildNumber
	$ubr = 0
	if ($cv.PSObject.Properties.Name -contains 'UBR') { $ubr = [int]$cv.UBR }
	$build = '{0}.{1}' -f $buildNo, $ubr
	$kind = ''
	if ($cv.PSObject.Properties.Name -contains 'InstallationType') { $kind = [string]$cv.InstallationType }
	if ($product -notmatch '^[A-Za-z0-9 .]{1,80}$') { $product = 'unknown' }
	if ($edition -notmatch '^[A-Za-z0-9]{1,40}$') { $edition = 'unknown' }
	if ($build -notmatch '^[0-9]{1,6}\.[0-9]{1,6}$') { $build = 'unknown' }
	if ($kind -notmatch '^[A-Za-z ]{1,40}$') { $kind = 'unknown' }
	[pscustomobject]@{ Product = $product; Edition = $edition; Build = $build; InstallationType = $kind }
}

function Get-ElevationType {
	if (-not ('WinunitdToken' -as [type])) {
		Add-Type -TypeDefinition @'
using System;
using System.Diagnostics;
using System.Runtime.InteropServices;
public static class WinunitdToken {
	[DllImport("advapi32.dll", SetLastError = true)]
	public static extern bool OpenProcessToken(IntPtr processHandle, uint desiredAccess, out IntPtr tokenHandle);
	[DllImport("advapi32.dll", SetLastError = true)]
	public static extern bool GetTokenInformation(IntPtr tokenHandle, int tokenInformationClass, ref int tokenInformation, int tokenInformationLength, out int returnLength);
	[DllImport("kernel32.dll", SetLastError = true)]
	public static extern bool CloseHandle(IntPtr handle);
	public static int Elevation() {
		IntPtr token;
		if (!OpenProcessToken(Process.GetCurrentProcess().Handle, 8, out token)) return 0;
		try {
			int value = 0;
			int needed;
			if (!GetTokenInformation(token, 18, ref value, 4, out needed)) return 0;
			return value;
		} finally {
			CloseHandle(token);
		}
	}
}
'@ -ErrorAction Stop
	}
	return [WinunitdToken]::Elevation()
}

function Get-IdentityLabel {
	$ident = [Security.Principal.WindowsIdentity]::GetCurrent()
	$principal = New-Object Security.Principal.WindowsPrincipal($ident)
	$system = New-Object Security.Principal.SecurityIdentifier ([Security.Principal.WellKnownSidType]::LocalSystemSid, $null)
	if ($ident.User.Value -eq $system.Value) { return 'SYSTEM' }
	$elevation = 0
	try { $elevation = [int](Get-ElevationType) } catch { $elevation = 0 }
	# TokenElevationTypeFull = 2, TokenElevationTypeLimited = 3.
	if ($elevation -eq 2) { return 'Administrator' }
	if ($elevation -eq 3) { return 'uac-filtered' }
	if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { return 'Administrator' }
	$admins = New-Object Security.Principal.SecurityIdentifier ([Security.Principal.WellKnownSidType]::BuiltinAdministratorsSid, $null)
	foreach ($group in $ident.Groups) {
		if ($group.Value -eq $admins.Value) { return 'uac-filtered' }
	}
	return 'standard-user'
}

function Test-InteractiveDesktop {
	if (-not [Environment]::UserInteractive) { return $false }
	try {
		$session = [int](Get-Process -Id $PID).SessionId
	} catch {
		return $false
	}
	return $session -gt 0
}

function Get-ClaimedSku([string]$Product, [string]$Edition, [string]$Build, [string]$InstallationType) {
	# Windows 11 starts at build 22000. Server 2022 is build 20348.
	# Server 2025 shares build 26100 with Windows 11 24H2, so a server
	# row also requires a Server product name or installation type.
	# Evaluation images and every other edition are unclaimed.
	if ($Edition -match 'Eval' -or $Product -match 'Evaluation') { return 'unclaimed' }
	$buildNum = 0
	if ($Build -match '^([0-9]+)\.') { $buildNum = [int]$Matches[1] }
	$client = $InstallationType -eq 'Client'
	$core = $InstallationType -eq 'Server Core'
	$server = $InstallationType -eq 'Server' -or $core
	if ($client -and $buildNum -ge 22000) {
		if ($Edition -eq 'Enterprise') { return 'Windows 11 Enterprise x64' }
		if ($Edition -eq 'EnterpriseS') { return 'Windows 11 Enterprise LTSC x64' }
	}
	if ($server) {
		$year = ''
		if ($Product -match 'Server 2022' -or $buildNum -eq 20348) { $year = '2022' }
		elseif ($Product -match 'Server 2025' -or ($buildNum -ge 26100 -and $Product -match 'Server')) { $year = '2025' }
		if (-not $year) { return 'unclaimed' }
		if ($core) { return 'Windows Server Core x64' }
		if ($year -eq '2022') { return 'Windows Server 2022 x64' }
		if ($year -eq '2025') { return 'Windows Server 2025 x64' }
	}
	return 'unclaimed'
}

function Test-OfflineGuest {
	# An empty route table is offline. Indeterminate only when NetTCPIP
	# cannot load. Get-NetRoute's empty-result error must not become that.
	try {
		Import-Module NetTCPIP -ErrorAction Stop
	} catch {
		return $null
	}
	$v4 = @(Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue)
	$v6 = @(Get-NetRoute -DestinationPrefix '::/0' -ErrorAction SilentlyContinue)
	return (@($v4).Count + @($v6).Count) -eq 0
}

function Get-DefaultRouteSnapshot {
	Import-Module NetTCPIP -ErrorAction Stop
	$items = New-Object System.Collections.Generic.List[object]
	foreach ($prefix in @('0.0.0.0/0', '::/0')) {
		foreach ($route in @(Get-NetRoute -DestinationPrefix $prefix -ErrorAction SilentlyContinue)) {
			[void]$items.Add([pscustomobject]@{
				DestinationPrefix = [string]$route.DestinationPrefix
				NextHop = [string]$route.NextHop
				InterfaceIndex = [int]$route.InterfaceIndex
				RouteMetric = [int]$route.RouteMetric
				AddressFamily = [string]$route.AddressFamily
			})
		}
	}
	return $items.ToArray()
}

function Restore-DefaultRoutes($Routes) {
	Import-Module NetTCPIP -ErrorAction Stop
	foreach ($route in @($Routes)) {
		if (-not $route) { continue }
		$alive = @(Get-NetRoute -DestinationPrefix $route.DestinationPrefix -InterfaceIndex $route.InterfaceIndex -ErrorAction SilentlyContinue)
		$present = $false
		foreach ($item in $alive) {
			if ([string]$item.NextHop -eq $route.NextHop) { $present = $true }
		}
		if ($present) { continue }
		New-NetRoute -DestinationPrefix $route.DestinationPrefix -InterfaceIndex $route.InterfaceIndex -NextHop $route.NextHop -RouteMetric $route.RouteMetric -AddressFamily $route.AddressFamily -Confirm:$false -ErrorAction Stop | Out-Null
	}
}

function Test-LocalPath([string]$Path) {
	try {
		$root = [IO.Path]::GetPathRoot($Path)
		if (-not $root -or $root.Length -lt 1) { return $false }
		$drive = New-Object System.IO.DriveInfo($root.Substring(0, 1))
		$kind = $drive.DriveType
		return $kind -eq [IO.DriveType]::Fixed -or $kind -eq [IO.DriveType]::Removable
	} catch {
		return $false
	}
}

function Get-RecordedCommit {
	if ($SourceCommit -and $SourceCommit -notmatch '^[0-9a-f]{7,40}$') { throw 'invalid source commit' }
	$manifest = Join-Path (Split-Path -Parent $MsiPath) 'package-manifest.json'
	if (-not (Test-Path -LiteralPath $manifest)) {
		if ($SourceCommit) { return $SourceCommit.ToLowerInvariant() }
		return ''
	}
	try {
		$parsed = Get-Content -LiteralPath $manifest -Raw -Encoding utf8 | ConvertFrom-Json
	} catch {
		throw 'package manifest could not be read'
	}
	if ($parsed.dirty) { throw 'package manifest is dirty' }
	if ([string]$parsed.installerVersion -ne $InstallerVersion) { throw 'package manifest version mismatch' }
	if ((ConvertTo-GuidText ([string]$parsed.upgradeCode)) -ne $UpgradeCode) { throw 'package manifest upgrade identity mismatch' }
	$commit = [string]$parsed.commit
	if ($commit -notmatch '^[0-9a-f]{7,40}$') { throw 'package manifest commit is not a revision' }
	if ($SourceCommit -and $SourceCommit.ToLowerInvariant() -ne $commit.ToLowerInvariant()) {
		throw 'source commit does not match the package manifest'
	}
	if ($parsed.PSObject.Properties.Name -contains 'sha256' -and [string]$parsed.sha256) {
		$want = ([string]$parsed.sha256).ToLowerInvariant()
		if ($want -notmatch '^[0-9a-f]{64}$') { throw 'package manifest hash is not a digest' }
		$got = (Get-FileHash -Algorithm SHA256 -LiteralPath $MsiPath).Hash.ToLowerInvariant()
		if ($got -ne $want) { throw 'package hash mismatch' }
		$script:PackageSha = $got
	}
	return $commit.ToLowerInvariant()
}

function Assert-PackageIdentity {
	$version = Get-MsiProperty $MsiPath 'ProductVersion'
	$upgrade = ConvertTo-GuidText (Get-MsiProperty $MsiPath 'UpgradeCode')
	$product = ConvertTo-GuidText (Get-MsiProperty $MsiPath 'ProductCode')
	if ($version -ne $InstallerVersion -or $upgrade -ne $UpgradeCode -or $product -ne $ProductCode) {
		throw 'package identity mismatch'
	}
}

function Test-OlderProductMsi([string]$Path) {
	$upgrade = ConvertTo-GuidText (Get-MsiProperty $Path 'UpgradeCode')
	$version = Get-MsiProperty $Path 'ProductVersion'
	if ($upgrade -ne $UpgradeCode) { return 'older MSI is not this product' }
	if (-not (Test-VersionLess $version $InstallerVersion)) { return 'older MSI is not an earlier ProductVersion' }
	return ''
}

function Get-PackageFiles([string]$Directory, [string]$Filter, [bool]$ChildDirectories) {
	$files = New-Object System.Collections.Generic.List[string]
	if (-not $Directory -or -not (Test-Path -LiteralPath $Directory)) { return $files.ToArray() }
	$dirs = @()
	if ($ChildDirectories) {
		$dirs = @(Get-ChildItem -LiteralPath $Directory -Directory -ErrorAction SilentlyContinue)
	} else {
		$dirs = @([pscustomobject]@{ FullName = $Directory })
	}
	foreach ($dir in $dirs) {
		if (-not $dir) { continue }
		foreach ($file in @(Get-ChildItem -LiteralPath $dir.FullName -File -Filter $Filter -ErrorAction SilentlyContinue)) {
			[void]$files.Add($file.FullName)
		}
	}
	return $files.ToArray()
}

function Test-BetaPackageFile([string]$Path) {
	try {
		$name = [IO.Path]::GetFileName($Path)
		if ($name -notmatch '^winunitd-\d+\.\d+\.\d+-x64-beta\.msi$') { return $false }
		$upgrade = ConvertTo-GuidText (Get-MsiProperty $Path 'UpgradeCode')
		if ($upgrade -ne $BetaUpgradeCode) { return $false }
		if ($null -eq (ConvertTo-MsiVersion (Get-MsiProperty $Path 'ProductVersion'))) { return $false }
		return $true
	} catch {
		return $false
	}
}

function Select-HighestMsi([string[]]$Paths) {
	$best = ''
	$bestVer = $null
	$tie = $false
	foreach ($path in @($Paths)) {
		if (-not $path) { continue }
		$ver = ConvertTo-MsiVersion (Get-MsiProperty $path 'ProductVersion')
		if ($null -eq $ver) { continue }
		if ($null -eq $bestVer -or $ver -gt $bestVer) {
			$bestVer = $ver
			$best = $path
			$tie = $false
		} elseif ($ver -eq $bestVer -and (ConvertTo-PathKey $path) -ne (ConvertTo-PathKey $best)) {
			$tie = $true
		}
	}
	if ($tie) { throw 'multiple packages share the selected version' }
	return $best
}

function Find-BetaMsi([string]$RepoRoot, [string]$ProductMsi) {
	$files = New-Object System.Collections.Generic.List[string]
	foreach ($path in @(Get-PackageFiles (Join-Path $RepoRoot 'dist\beta') 'winunitd-*-x64-beta.msi' $true)) { [void]$files.Add($path) }
	foreach ($path in @(Get-PackageFiles (Join-Path $RepoRoot 'packaging\beta') 'winunitd-*-x64-beta.msi' $false)) { [void]$files.Add($path) }
	$sibling = Split-Path -Parent $ProductMsi
	foreach ($path in @(Get-PackageFiles $sibling 'winunitd-*-x64-beta.msi' $false)) { [void]$files.Add($path) }
	$valid = New-Object System.Collections.Generic.List[string]
	$seen = @{}
	foreach ($path in $files) {
		if (-not $path) { continue }
		$key = ConvertTo-PathKey $path
		if ($seen.ContainsKey($key)) { continue }
		$seen[$key] = $true
		if (Test-BetaPackageFile $path) { [void]$valid.Add($path) }
	}
	if ($valid.Count -eq 0) { return '' }
	return Select-HighestMsi $valid.ToArray()
}

function Find-OlderProductMsi([string]$RepoRoot, [string]$ProductMsi) {
	$files = New-Object System.Collections.Generic.List[string]
	foreach ($path in @(Get-PackageFiles (Join-Path $RepoRoot 'dist\wix') 'winunitd-*-x64.msi' $true)) { [void]$files.Add($path) }
	foreach ($path in @(Get-PackageFiles (Join-Path $RepoRoot 'packaging\wix') 'winunitd-*-x64.msi' $false)) { [void]$files.Add($path) }
	$sibling = Split-Path -Parent $ProductMsi
	foreach ($path in @(Get-PackageFiles $sibling 'winunitd-*-x64.msi' $false)) { [void]$files.Add($path) }
	$productKey = ConvertTo-PathKey $ProductMsi
	$valid = New-Object System.Collections.Generic.List[string]
	$seen = @{}
	foreach ($path in $files) {
		if (-not $path) { continue }
		$name = [IO.Path]::GetFileName($path)
		if ($name -notmatch '^winunitd-\d+\.\d+\.\d+-x64\.msi$') { continue }
		$key = ConvertTo-PathKey $path
		if ($key -eq $productKey) { continue }
		if ($seen.ContainsKey($key)) { continue }
		$seen[$key] = $true
		try {
			if (-not (Test-OlderProductMsi $path)) { [void]$valid.Add($path) }
		} catch { }
	}
	if ($valid.Count -eq 0) { return '' }
	return Select-HighestMsi $valid.ToArray()
}

function Get-SkipReason($Spec) {
	$req = $Spec.requires
	if ($req.gui_sku -and $script:Guest.InstallationType -eq 'Server Core') {
		return 'NOT_APPLICABLE:Server Core has no interactive GUI'
	}
	if ($req.identity -eq 'system' -and $script:Identity -ne 'SYSTEM') { return 'current identity is not SYSTEM' }
	if ($req.identity -eq 'elevated' -and $script:Identity -ne 'SYSTEM' -and $script:Identity -ne 'Administrator') { return 'current identity is not elevated' }
	# non-admin drops an elevated parent to fixture user alice.
	if ($req.identity -eq 'not-elevated' -and $Case -ne 'non-admin' -and ($script:Identity -eq 'SYSTEM' -or $script:Identity -eq 'Administrator')) { return 'current identity is elevated' }
	if ($req.interactive -and -not $script:Interactive) { return 'no interactive session: process is not user-interactive in a desktop session (session id greater than 0)' }
	if ($req.offline) {
		if ($null -eq $script:Offline) { return 'could not determine default route: NetTCPIP module did not load' }
	}
	$present = Test-ProductPresent
	if ($req.product -eq 'absent' -and $present) { return 'product is already installed' }
	if ($req.product -eq 'present' -and -not $present) { return 'product is not installed' }
	if ($req.older_msi -and -not $OlderMsi) { return 'no recorded N-1 package' }
	if ($req.beta_msi -and -not $BetaMsi) { return 'beta package was not supplied' }
	return ''
}

function ConvertTo-Plain($Value) {
	if ($Value -is [System.Collections.IDictionary]) {
		$copy = @{}
		foreach ($key in @($Value.Keys)) {
			$copy[[string]$key] = ConvertTo-Plain $Value[$key]
		}
		return $copy
	}
	return $Value
}

function Assert-Redacted([string]$Json) {
	$wellKnown = @('SYSTEM', 'LOCAL SERVICE', 'NETWORK SERVICE', 'Administrator')
	foreach ($localName in @([string]$env:COMPUTERNAME, [string]$env:USERNAME)) {
		if ($localName.Length -lt 3) { continue }
		if ($wellKnown -contains $localName) { continue }
		$pattern = '"' + [regex]::Escape($localName) + '"'
		if ([regex]::IsMatch($Json, $pattern, [System.Text.RegularExpressions.RegexOptions]::IgnoreCase)) {
			throw 'summary failed redaction'
		}
	}
	if ($Json -match 'S-1-5-21-\d') { throw 'summary failed redaction' }
	if ($Json -match '\b(?:\d{1,3}\.){3}\d{1,3}\b') { throw 'summary failed redaction' }
	if ($Json.Contains('\\')) { throw 'summary failed redaction' }
}

function Write-Summary($Result) {
	$markers = @(Get-LogMarkers $Result.Log)
	$prerequisite = $false
	if ($Result.Log) { $prerequisite = Test-PrerequisiteAction $Result.Log }
	$event = Get-EventSourceState
	$layout = Test-Layout
	$pathOwned = Test-PathOwned
	$obj = [ordered]@{
		schema = 1
		evidence_id = [string]$EvidenceId
		source_commit = [string]$script:Commit
		installer_version = $InstallerVersion
		product_code = $ProductCode
		upgrade_code = $UpgradeCode
		package_sha256 = [string]$script:PackageSha
		guest_product = [string]$script:Guest.Product
		guest_edition = [string]$script:Guest.Edition
		guest_build = [string]$script:Guest.Build
		installation_type = [string]$script:Guest.InstallationType
		guest_sku = [string]$script:GuestSku
		identity = $(if ($script:SummaryIdentity) { [string]$script:SummaryIdentity } else { [string]$script:Identity })
		offline = $script:Offline
		interactive = [bool]$script:Interactive
		case_id = $Case
		status = [string]$Result.Status
		msi_exit = $Result.Exit
		service_before = [string]$Result.Before
		service_after = [string]$Result.After
		payload_sha256 = (Get-PayloadHashes)
		log_markers = $markers
		reboot_started = [bool]$Result.Reboot
		layout_ok = [bool]$layout
		event_source = [string]$event
		path_owned = [bool]$pathOwned
		prerequisite_action = [bool]$prerequisite
		data_retained = $Result.DataRetained
		note = [string]$Result.Note
	}
	$plain = ConvertTo-Plain $obj
	try {
		Add-Type -AssemblyName System.Web.Extensions
		$serializer = New-Object System.Web.Script.Serialization.JavaScriptSerializer
		$serializer.MaxJsonLength = 67108864
		$json = $serializer.Serialize($plain)
	} catch {
		$json = $plain | ConvertTo-Json -Compress -Depth 6
	}
	Assert-Redacted $json
	$dest = Join-Path $EvidenceDirectory 'acceptance-summary.jsonl'
	$utf8 = New-Object System.Text.UTF8Encoding $false
	[IO.File]::AppendAllText($dest, $json + "`n", $utf8)
	$exitText = 'none'
	if ($null -ne $Result.Exit) { $exitText = [string]$Result.Exit }
	Write-Output ("{0} {1} exit={2} service={3}->{4} reboot_started={5}" -f $Case, $Result.Status, $exitText, $Result.Before, $Result.After, [bool]$Result.Reboot)
}

function Invoke-Winctl([string[]]$Words) {
	$out = Join-Path $EvidenceDirectory 'winctl-out.txt'
	$err = Join-Path $EvidenceDirectory 'winctl-err.txt'
	foreach ($path in @($out, $err)) {
		if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }
	}
	$proc = Start-Process -FilePath (Get-Winctl) -ArgumentList $Words -PassThru -Wait -WindowStyle Hidden -RedirectStandardOutput $out -RedirectStandardError $err
	$text = ''
	if (Test-Path -LiteralPath $out) { $text += [IO.File]::ReadAllText($out) }
	if (Test-Path -LiteralPath $err) { $text += [IO.File]::ReadAllText($err) }
	[pscustomobject]@{ ExitCode = [int]$proc.ExitCode; Text = $text }
}

function Get-UnitReport([string]$Name) {
	$report = Invoke-Winctl @('status', $Name)
	$active = 'unknown'
	$enabled = 'unknown'
	if ($report.Text -match '(?m)^\s*Active:\s*(\S+)') { $active = $Matches[1] }
	if ($report.Text -match '; enabled\)') { $enabled = 'enabled' }
	elseif ($report.Text -match '; disabled\)') { $enabled = 'disabled' }
	[pscustomobject]@{ Active = $active; Enabled = $enabled }
}

function Write-FixtureUnit([string]$Name, [string]$Label) {
	$dir = Join-Path (Get-DataRoot) 'units'
	New-Item -ItemType Directory -Force -Path $dir | Out-Null
	$body = @"
[Unit]
Description=Acceptance fixture $Label

[Service]
Type=simple
ExecStart=["C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "while (`$true) { Write-Output '$Label fixture'; Start-Sleep -Seconds 5 }"]
WorkingDirectory=C:\Windows
Restart=on-failure
RestartSec=2s
TimeoutStopSec=10s

[Install]
WantedBy=default.target
"@
	Set-Content -LiteralPath (Join-Path $dir ($Name)) -Value $body -Encoding ascii
}

function Stop-FixtureProcesses {
	foreach ($proc in @(Get-CimInstance Win32_Process -Filter "Name='powershell.exe'")) {
		$cmd = [string]$proc.CommandLine
		if ($cmd -like '*alice fixture*' -or $cmd -like '*bob fixture*') {
			Stop-Process -Id $proc.ProcessId -Force -ErrorAction SilentlyContinue
		}
	}
}

function Remove-FixtureUnits {
	$root = Get-DataRoot
	foreach ($name in @('alice.service', 'bob.service')) {
		foreach ($rel in @("units\$name", "enabled\default.target\$name")) {
			$path = Join-Path $root $rel
			if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }
		}
	}
}

function Set-DelayedStart([bool]$Delayed) {
	$mode = 'auto'
	if ($Delayed) { $mode = 'delayed-auto' }
	& cmd.exe /c ('sc.exe config winunitd start= ' + $mode) | Out-Null
	return $LASTEXITCODE -eq 0
}

function Get-DelayedStart {
	$key = 'HKLM:\SYSTEM\CurrentControlSet\Services\winunitd'
	if (-not (Test-Path -LiteralPath $key)) { return $false }
	$prop = Get-ItemProperty -LiteralPath $key
	if ($prop.PSObject.Properties.Name -notcontains 'DelayedAutostart') { return $false }
	return [int]$prop.DelayedAutostart -eq 1
}

function Invoke-CleanInstall([string]$LogName, [bool]$Quiet, [int]$TimeoutSec, [bool]$BasicUi = $false) {
	$before = Get-ServiceState
	$run = Invoke-Msiexec -LogName $LogName -Quiet:$Quiet -BasicUi:$BasicUi -TimeoutSec $TimeoutSec -Words @('/i', (ConvertTo-MsiArg $MsiPath))
	$note = Get-InstallFailureNote $run
	$status = 'passed'
	if ($note) { $status = 'failed' }
	return New-CaseResult $status $note $run.ExitCode $run.Log $run.Reboot $before (Get-ServiceState) $null
}

function Invoke-OfflineInstall {
	$before = Get-ServiceState
	if (-not (Test-LocalPath $MsiPath) -or -not (Test-LocalPath $EvidenceDirectory)) {
		return New-CaseResult 'failed' 'offline case requires a local package and evidence directory' $null '' $false $before $before $null
	}
	$removed = New-Object System.Collections.Generic.List[object]
	$script:RouteRestoreNote = ''
	$result = $null
	try {
		try {
			foreach ($route in @(Get-DefaultRouteSnapshot)) {
				if (-not $route) { continue }
				[void]$removed.Add($route)
				Remove-NetRoute -DestinationPrefix $route.DestinationPrefix -InterfaceIndex $route.InterfaceIndex -NextHop $route.NextHop -Confirm:$false -ErrorAction Stop | Out-Null
			}
		} catch {
			$result = New-CaseResult 'failed' 'could not remove default route' $null '' $false $before (Get-ServiceState) $null
		}
		if (-not $result) {
			$script:Offline = Test-OfflineGuest
			if ($script:Offline -ne $true) {
				$result = New-CaseResult 'failed' 'default route remained' $null '' $false $before (Get-ServiceState) $null
			} else {
				$result = Invoke-CleanInstall 'offline-install' $true 360
			}
		}
	} catch {
		$failure = $_
		if (-not $result) {
			$note = 'could not remove default route'
			if ($failure.Exception.Message -eq 'msiexec timed out') { $note = 'msiexec timed out' }
			$result = New-CaseResult 'failed' $note $null '' $false $before (Get-ServiceState) $null
		}
	} finally {
		if ($removed.Count -gt 0) {
			try { Restore-DefaultRoutes $removed.ToArray() } catch { $script:RouteRestoreNote = 'default route restore failed' }
		}
	}
	if (-not $result) { $result = New-CaseResult 'failed' 'could not remove default route' $null '' $false $before (Get-ServiceState) $null }
	if ($script:RouteRestoreNote) {
		$result.Status = 'failed'
		if (-not $result.Note) { $result.Note = $script:RouteRestoreNote }
		else { $result.Note = $result.Note + '; ' + $script:RouteRestoreNote }
	}
	return $result
}

function Invoke-Repair([string]$LogName, [string[]]$Words) {
	$before = Get-ServiceState
	$run = Invoke-Msiexec -LogName $LogName -Quiet -TimeoutSec 600 -Words $Words
	$note = Get-InstallFailureNote $run
	$status = 'passed'
	if ($note) { $status = 'failed' }
	return New-CaseResult $status $note $run.ExitCode $run.Log $run.Reboot $before (Get-ServiceState) $null
}

function Invoke-SelectedCase {
	switch ($Case) {
		'quiet-install' { return Invoke-CleanInstall 'quiet-install' $true 360 }
		'gui-install' { return Invoke-CleanInstall 'gui-install' $false 900 $true }
		'system-install' { return Invoke-CleanInstall 'system-install' $true 360 }
		'offline-install' { return Invoke-OfflineInstall }
		'repair-fa' { return Invoke-Repair 'repair-fa' @('/fa', (ConvertTo-MsiArg $MsiPath)) }
		'repair-reinstall' { return Invoke-Repair 'repair-reinstall' @('/i', (ConvertTo-MsiArg $MsiPath), 'REINSTALL=ALL', 'REINSTALLMODE=amus') }
		'uninstall' { return Invoke-Uninstall }
		'reinstall-retained' { return Invoke-ReinstallRetained }
		'downgrade' { return Invoke-Downgrade }
		'n1-upgrade' { return Invoke-N1Upgrade }
		'locked-file' { return Invoke-LockedFile }
		'rollback-test-fail-running' { return Invoke-TestFailRepair $true }
		'rollback-test-fail-stopped' { return Invoke-TestFailRepair $false }
		'rollback-upgrade' { return Invoke-RollbackUpgrade }
		'non-admin' { return Invoke-NonAdmin }
		'beta-conflict' { return Invoke-BetaConflict }
		'preflight-reparse' { return Invoke-PreflightReparse }
		'preflight-unmanaged-service' { return Invoke-PreflightUnmanaged }
		default { throw 'case is not implemented' }
	}
}

function Invoke-Uninstall {
	$before = Get-ServiceState
	$markerDir = Join-Path (Get-DataRoot) 'units'
	New-Item -ItemType Directory -Force -Path $markerDir | Out-Null
	$marker = Join-Path $markerDir 'carol.marker'
	Set-Content -LiteralPath $marker -Value 'retained' -Encoding ascii
	$pathBefore = @(Get-PathSegments)
	$run = Invoke-Msiexec -LogName 'uninstall' -Quiet -TimeoutSec 600 -Words @('/x', (ConvertTo-MsiArg $MsiPath))
	$note = ''
	if ($run.Reboot) { $note = 'reboot started' }
	elseif ($run.ExitCode -ne 0) { $note = 'unexpected exit' }
	elseif ((Get-ServiceState) -ne 'absent') { $note = 'service remained' }
	elseif (Test-ProductPresent) { $note = 'product remained' }
	elseif ((Get-EventSourceState) -ne 'absent') { $note = 'event source remained' }
	elseif (Test-PathOwned) { $note = 'PATH ownership remained' }
	else {
		$pathNote = Test-PathRemoval $pathBefore @(Get-PathSegments)
		if ($pathNote) { $note = $pathNote }
		elseif (-not (Test-Path -LiteralPath (Get-DataRoot))) { $note = 'data directory was removed' }
		elseif ((Get-Content -LiteralPath $marker -Raw).Trim() -ne 'retained') { $note = 'retained marker changed' }
		elseif (Test-PrerequisiteAction $run.Log) { $note = 'destination prerequisite invoked' }
	}
	$status = 'passed'
	if ($note) { $status = 'failed' }
	$retained = $false
	if (-not $note) { $retained = $true }
	return New-CaseResult $status $note $run.ExitCode $run.Log $run.Reboot $before (Get-ServiceState) $retained
}

function Invoke-ReinstallRetained {
	$before = Get-ServiceState
	if (-not (Start-Winunitd)) { return New-CaseResult 'failed' 'service is not running' $null '' $false $before (Get-ServiceState) $null }
	Write-FixtureUnit 'alice.service' 'alice'
	Write-FixtureUnit 'bob.service' 'bob'
	$script:FixturesWritten = $true
	$alicePath = Join-Path (Get-DataRoot) 'units\alice.service'
	$bobPath = Join-Path (Get-DataRoot) 'units\bob.service'
	$aliceHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $alicePath).Hash.ToLowerInvariant()
	$bobHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $bobPath).Hash.ToLowerInvariant()
	$reload = Invoke-Winctl @('daemon-reload')
	if ($reload.ExitCode -ne 0) { return New-CaseResult 'failed' 'fixture reload failed' $null '' $false $before (Get-ServiceState) $null }
	$enable = Invoke-Winctl @('enable', 'alice.service')
	if ($enable.ExitCode -ne 0) { return New-CaseResult 'failed' 'fixture enable failed' $null '' $false $before (Get-ServiceState) $null }
	foreach ($name in @('alice.service', 'bob.service')) {
		$start = Invoke-Winctl @('start', $name)
		if ($start.ExitCode -ne 0) { return New-CaseResult 'failed' 'fixture start failed' $null '' $false $before (Get-ServiceState) $null }
	}
	$readyNote = Wait-FixtureState $true $true
	if ($readyNote) { return New-CaseResult 'failed' $readyNote $null '' $false $before (Get-ServiceState) $null }
	$remove = Invoke-Msiexec -LogName 'reinstall-retained-remove' -Quiet -TimeoutSec 600 -Words @('/x', (ConvertTo-MsiArg $MsiPath))
	if ($remove.ExitCode -ne 0 -or $remove.Reboot) { return New-CaseResult 'failed' 'uninstall failed' $remove.ExitCode $remove.Log $remove.Reboot $before (Get-ServiceState) $false }
	if ((Get-FileHash -Algorithm SHA256 -LiteralPath $alicePath).Hash.ToLowerInvariant() -ne $aliceHash) { return New-CaseResult 'failed' 'retained unit changed' $remove.ExitCode $remove.Log $remove.Reboot $before (Get-ServiceState) $false }
	if ((Get-FileHash -Algorithm SHA256 -LiteralPath $bobPath).Hash.ToLowerInvariant() -ne $bobHash) { return New-CaseResult 'failed' 'retained unit changed' $remove.ExitCode $remove.Log $remove.Reboot $before (Get-ServiceState) $false }
	$aliceLink = Join-Path (Get-DataRoot) 'enabled\default.target\alice.service'
	$bobLink = Join-Path (Get-DataRoot) 'enabled\default.target\bob.service'
	if (-not (Test-Path -LiteralPath $aliceLink)) { return New-CaseResult 'failed' 'enable link was removed' $remove.ExitCode $remove.Log $remove.Reboot $before (Get-ServiceState) $false }
	if (Test-Path -LiteralPath $bobLink) { return New-CaseResult 'failed' 'manual unit became enabled' $remove.ExitCode $remove.Log $remove.Reboot $before (Get-ServiceState) $false }
	$run = Invoke-Msiexec -LogName 'reinstall-retained' -Quiet -TimeoutSec 600 -Words @('/i', (ConvertTo-MsiArg $MsiPath))
	$note = Get-InstallFailureNote $run
	if (-not $note) {
		if ((Get-FileHash -Algorithm SHA256 -LiteralPath $alicePath).Hash.ToLowerInvariant() -ne $aliceHash -or (Get-FileHash -Algorithm SHA256 -LiteralPath $bobPath).Hash.ToLowerInvariant() -ne $bobHash) {
			$note = 'retained unit changed'
		} else {
			$note = Wait-FixtureState $false $false
		}
	}
	$status = 'passed'
	if ($note) { $status = 'failed' }
	$retained = ($note -eq '' -or $note -eq 'fixture cleanup failed')
	if (-not $note) {
		$cleanup = Clear-Fixtures
		if ($cleanup) {
			$note = 'fixture cleanup failed'
			$status = 'failed'
			$retained = $true
		}
	}
	return New-CaseResult $status $note $run.ExitCode $run.Log $run.Reboot $before (Get-ServiceState) $retained
}

function Wait-FixtureState([bool]$BobActive, [bool]$BeforeRemove) {
	$deadline = (Get-Date).AddSeconds(60)
	do {
		$alice = Get-UnitReport 'alice.service'
		$bob = Get-UnitReport 'bob.service'
		$aliceReady = $alice.Active -eq 'active' -and $alice.Enabled -eq 'enabled'
		if ($BobActive) {
			$bobReady = $bob.Active -eq 'active' -and $bob.Enabled -eq 'disabled'
		} else {
			$bobReady = $bob.Active -ne 'active' -and $bob.Enabled -eq 'disabled'
		}
		if ($aliceReady -and $bobReady) { return '' }
		if ((Get-Date) -ge $deadline) { break }
		Start-Sleep -Seconds 1
	} while ($true)
	if ($BeforeRemove) { return 'fixture did not reach the prepared state' }
	if ($BobActive) { return 'fixture did not reach the prepared state' }
	$bob = Get-UnitReport 'bob.service'
	if ($bob.Active -eq 'active') { return 'manual unit was restored' }
	if ($bob.Enabled -ne 'disabled') { return 'manual unit became enabled' }
	return 'enabled unit did not start'
}

function Clear-Fixtures {
	try {
		if ((Get-ServiceState) -eq 'running') {
			Invoke-Winctl @('stop', 'alice.service') | Out-Null
			Invoke-Winctl @('stop', 'bob.service') | Out-Null
			Invoke-Winctl @('disable', 'alice.service') | Out-Null
		}
		Stop-FixtureProcesses
		Remove-FixtureUnits
		return ''
	} catch {
		return 'fixture cleanup failed'
	}
}

function Invoke-Downgrade {
	$reason = Test-OlderProductMsi $OlderMsi
	if ($reason) { return New-CaseResult 'failed' $reason $null '' $false (Get-ServiceState) (Get-ServiceState) $null }
	$before = Get-ServiceState
	$hash = ''
	$exe = Get-ProductExe
	if (Test-Path -LiteralPath $exe) { $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $exe).Hash.ToLowerInvariant() }
	$run = Invoke-Msiexec -LogName 'downgrade' -Quiet -TimeoutSec 600 -Words @('/i', (ConvertTo-MsiArg $OlderMsi))
	$note = ''
	if ($run.Reboot) { $note = 'reboot started' }
	elseif ($run.ExitCode -ne 1603) { $note = 'unexpected exit' }
	elseif (-not (Test-ProductPresent)) { $note = 'product was removed' }
	elseif ($hash -and (Get-FileHash -Algorithm SHA256 -LiteralPath $exe).Hash.ToLowerInvariant() -ne $hash) { $note = 'payload hash changed' }
	elseif ((Get-ServiceState) -ne $before) { $note = 'service state changed' }
	return New-CaseResult $(if ($note) { 'failed' } else { 'passed' }) $note $run.ExitCode $run.Log $run.Reboot $before (Get-ServiceState) $null
}

function Invoke-N1Upgrade {
	$before = Get-ServiceState
	$reason = Test-OlderProductMsi $OlderMsi
	if ($reason) { return New-CaseResult 'failed' $reason $null '' $false $before $before $null }
	$older = Invoke-Msiexec -LogName 'n1-upgrade-older' -Quiet -TimeoutSec 600 -Words @('/i', (ConvertTo-MsiArg $OlderMsi))
	if ($older.ExitCode -ne 0 -or $older.Reboot) { return New-CaseResult 'failed' 'older package install failed' $older.ExitCode $older.Log $older.Reboot $before (Get-ServiceState) $null }
	if (-not (Test-Path -LiteralPath (Get-ProductExe))) { return New-CaseResult 'failed' 'older package layout mismatch' $older.ExitCode $older.Log $older.Reboot $before (Get-ServiceState) $null }
	$run = Invoke-Msiexec -LogName 'n1-upgrade' -Quiet -TimeoutSec 600 -Words @('/i', (ConvertTo-MsiArg $MsiPath))
	$note = Get-InstallFailureNote $run
	$status = 'passed'
	if ($note) { $status = 'failed' }
	return New-CaseResult $status $note $run.ExitCode $run.Log $run.Reboot $before (Get-ServiceState) $null
}

function Invoke-LockedFile {
	$beforeStop = Get-ServiceState
	if (-not (Stop-Winunitd)) { return New-CaseResult 'failed' 'service did not stop' $null '' $false $beforeStop (Get-ServiceState) $null }
	$before = Get-ServiceState
	$exe = Get-ProductExe
	$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $exe).Hash.ToLowerInvariant()
	$stream = $null
	$opened = $false
	try {
		for ($i = 0; $i -lt 30 -and -not $opened; $i++) {
			try {
				$stream = [IO.File]::Open($exe, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::None)
				$opened = $true
			} catch {
				Start-Sleep -Seconds 1
			}
		}
		if (-not $opened) { return New-CaseResult 'failed' 'could not lock payload' $null '' $false $before (Get-ServiceState) $null }
		$run = Invoke-Msiexec -LogName 'locked-file' -Quiet -TimeoutSec 600 -Words @('/fa', (ConvertTo-MsiArg $MsiPath))
		$note = ''
		if ($run.Reboot) { $note = 'reboot started' }
		elseif ($run.ExitCode -ne 1603) { $note = 'unexpected exit' }
		elseif ((Get-ServiceState) -ne 'stopped') { $note = 'service state changed' }
		if ($stream) {
			$stream.Dispose()
			$stream = $null
		}
		if (-not $note -and (Get-FileHash -Algorithm SHA256 -LiteralPath $exe).Hash.ToLowerInvariant() -ne $hash) {
			$note = 'payload hash changed'
		}
		return New-CaseResult $(if ($note) { 'failed' } else { 'passed' }) $note $run.ExitCode $run.Log $run.Reboot $before (Get-ServiceState) $null
	} finally {
		if ($stream) { $stream.Dispose() }
	}
}

function Invoke-TestFailRepair([bool]$Running) {
	$changed = $false
	try {
		if ($Running) {
			if (-not (Start-Winunitd)) { return New-CaseResult 'failed' 'service is not running' $null '' $false (Get-ServiceState) (Get-ServiceState) $null }
		} else {
			if (-not (Stop-Winunitd)) { return New-CaseResult 'failed' 'service did not stop' $null '' $false (Get-ServiceState) (Get-ServiceState) $null }
		}
		if (-not (Set-DelayedStart $true)) { return New-CaseResult 'failed' 'could not set delayed start' $null '' $false (Get-ServiceState) (Get-ServiceState) $null }
		$changed = $true
		$before = Get-ServiceState
		$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath (Get-ProductExe)).Hash.ToLowerInvariant()
		$run = Invoke-Msiexec -LogName $(if ($Running) { 'rollback-test-fail-running' } else { 'rollback-test-fail-stopped' }) -Quiet -TestFail -TimeoutSec 600 -Words @('/fa', (ConvertTo-MsiArg $MsiPath))
		$delayedOk = Get-DelayedStart
		$note = ''
		if ($run.Reboot) { $note = 'reboot started' }
		elseif ($run.ExitCode -ne 1603) { $note = 'unexpected exit' }
		elseif ((Get-FileHash -Algorithm SHA256 -LiteralPath (Get-ProductExe)).Hash.ToLowerInvariant() -ne $hash) { $note = 'payload hash changed' }
		elseif (-not $delayedOk) { $note = 'service configuration was not restored' }
		elseif ((Get-ServiceState) -ne $before) { $note = 'service state was not restored' }
		return New-CaseResult $(if ($note) { 'failed' } else { 'passed' }) $note $run.ExitCode $run.Log $run.Reboot $before (Get-ServiceState) $null
	} finally {
		if ($changed) { Set-DelayedStart $false | Out-Null }
	}
}

function Invoke-RollbackUpgrade {
	$before = Get-ServiceState
	$reason = Test-OlderProductMsi $OlderMsi
	if ($reason) { return New-CaseResult 'failed' $reason $null '' $false $before $before $null }
	$older = Invoke-Msiexec -LogName 'rollback-upgrade-older' -Quiet -TimeoutSec 600 -Words @('/i', (ConvertTo-MsiArg $OlderMsi))
	if ($older.ExitCode -ne 0 -or $older.Reboot) { return New-CaseResult 'failed' 'older package install failed' $older.ExitCode $older.Log $older.Reboot $before (Get-ServiceState) $null }
	if (-not (Start-Winunitd)) { return New-CaseResult 'failed' 'service is not running' $older.ExitCode $older.Log $older.Reboot $before (Get-ServiceState) $null }
	if (-not (Set-DelayedStart $true)) { return New-CaseResult 'failed' 'could not set delayed start' $null '' $false (Get-ServiceState) (Get-ServiceState) $null }
	$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath (Get-ProductExe)).Hash.ToLowerInvariant()
	$serviceBefore = Get-ServiceState
	try {
		$run = Invoke-Msiexec -LogName 'rollback-upgrade' -Quiet -TestFail -TimeoutSec 600 -Words @('/i', (ConvertTo-MsiArg $MsiPath))
		$note = ''
		if ($run.Reboot) { $note = 'reboot started' }
		elseif ($run.ExitCode -ne 1603) { $note = 'unexpected exit' }
		elseif ((Get-FileHash -Algorithm SHA256 -LiteralPath (Get-ProductExe)).Hash.ToLowerInvariant() -ne $hash) { $note = 'payload hash changed' }
		elseif (-not (Get-DelayedStart)) { $note = 'service configuration was not restored' }
		elseif ((Get-ServiceState) -ne $serviceBefore) { $note = 'service state was not restored' }
		return New-CaseResult $(if ($note) { 'failed' } else { 'passed' }) $note $run.ExitCode $run.Log $run.Reboot $serviceBefore (Get-ServiceState) $null
	} finally {
		if (Get-Service -Name winunitd -ErrorAction SilentlyContinue) { Set-DelayedStart $false | Out-Null }
	}
}

function New-FixturePassword {
	$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
	try {
		$bytes = New-Object byte[] 24
		$rng.GetBytes($bytes)
		return 'Aa1!' + [Convert]::ToBase64String($bytes)
	} finally {
		$rng.Dispose()
	}
}

function Add-AccessRule([string]$Path, [Security.Principal.IdentityReference]$Identity, [string]$Rights, [bool]$Container) {
	$acl = Get-Acl -LiteralPath $Path
	$flags = [Security.AccessControl.InheritanceFlags]::None
	if ($Container) { $flags = [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit' }
	$prop = [Security.AccessControl.PropagationFlags]::None
	$right = [Security.AccessControl.FileSystemRights]$Rights
	$rule = New-Object System.Security.AccessControl.FileSystemAccessRule($Identity, $right, $flags, $prop, 'Allow')
	$acl.AddAccessRule($rule)
	Set-Acl -LiteralPath $Path -AclObject $acl
	return $rule
}

function Remove-AccessRule([string]$Path, $Rule) {
	if (-not $Rule -or -not (Test-Path -LiteralPath $Path)) { return }
	$acl = Get-Acl -LiteralPath $Path
	[void]$acl.RemoveAccessRule($Rule)
	Set-Acl -LiteralPath $Path -AclObject $acl
}

function Test-FixtureUserElevated([Security.Principal.SecurityIdentifier]$Sid) {
	Import-Module Microsoft.PowerShell.LocalAccounts -ErrorAction Stop
	foreach ($member in @(Get-LocalGroupMember -Group 'Administrators' -ErrorAction SilentlyContinue)) {
		if (-not $member) { continue }
		if ([string]$member.SID -eq $Sid.Value) { return $true }
	}
	return $false
}

function Invoke-NonAdminResult($Run, [string]$Before) {
	$allowed = @(1602, 1603, 1625)
	$note = ''
	if ($Run.Reboot) { $note = 'reboot started' }
	elseif ($allowed -notcontains $Run.ExitCode) { $note = 'unexpected exit' }
	elseif (Test-ProductPresent) { $note = 'product was installed' }
	elseif ((Get-ServiceState) -ne 'absent') { $note = 'service remained' }
	$status = 'passed'
	if ($note) { $status = 'failed' }
	return New-CaseResult $status $note $Run.ExitCode $Run.Log $Run.Reboot $Before (Get-ServiceState) $null
}

function Ensure-FixtureType {
	if ('WinunitdFixture' -as [type]) { return }
	Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security;
using System.Text;

public static class WinunitdFixture {
	const uint TOKEN_ADJUST_PRIVILEGES = 0x0020;
	const uint TOKEN_QUERY = 0x0008;
	const uint SE_PRIVILEGE_ENABLED = 0x00000002;
	const uint POLICY_LOOKUP_NAMES = 0x00000800;
	const uint POLICY_CREATE_ACCOUNT = 0x00000010;

	[StructLayout(LayoutKind.Sequential)]
	struct LUID {
		public uint LowPart;
		public int HighPart;
	}

	[StructLayout(LayoutKind.Sequential)]
	struct LUID_AND_ATTRIBUTES {
		public LUID Luid;
		public uint Attributes;
	}

	[StructLayout(LayoutKind.Sequential)]
	struct TOKEN_PRIVILEGES {
		public int Count;
		public LUID_AND_ATTRIBUTES Priv;
	}

	[StructLayout(LayoutKind.Sequential)]
	struct LSA_UNICODE_STRING {
		public ushort Length;
		public ushort MaximumLength;
		public IntPtr Buffer;
	}

	[StructLayout(LayoutKind.Sequential)]
	struct LSA_OBJECT_ATTRIBUTES {
		public int Length;
		public IntPtr RootDirectory;
		public IntPtr ObjectName;
		public uint Attributes;
		public IntPtr SecurityDescriptor;
		public IntPtr SecurityQualityOfService;
	}

	[StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
	struct STARTUPINFO {
		public int cb;
		public IntPtr lpReserved;
		public IntPtr lpDesktop;
		public IntPtr lpTitle;
		public int dwX;
		public int dwY;
		public int dwXSize;
		public int dwYSize;
		public int dwXCountChars;
		public int dwYCountChars;
		public int dwFillAttribute;
		public int dwFlags;
		public short wShowWindow;
		public short cbReserved2;
		public IntPtr lpReserved2;
		public IntPtr hStdInput;
		public IntPtr hStdOutput;
		public IntPtr hStdError;
	}

	[StructLayout(LayoutKind.Sequential)]
	struct PROCESS_INFORMATION {
		public IntPtr hProcess;
		public IntPtr hThread;
		public int dwProcessId;
		public int dwThreadId;
	}

	[StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
	struct PROFILEINFO {
		public int dwSize;
		public int dwFlags;
		public string lpUserName;
		public string lpProfilePath;
		public string lpDefaultPath;
		public string lpServerName;
		public string lpPolicyPath;
		public IntPtr hProfile;
	}

	[DllImport("kernel32.dll", SetLastError = true)]
	static extern bool CloseHandle(IntPtr handle);

	[DllImport("kernel32.dll", SetLastError = true)]
	static extern IntPtr LocalFree(IntPtr memory);

	[DllImport("kernel32.dll", SetLastError = true)]
	static extern uint WaitForSingleObject(IntPtr handle, uint milliseconds);

	[DllImport("kernel32.dll", SetLastError = true)]
	static extern bool GetExitCodeProcess(IntPtr process, out uint exitCode);

	[DllImport("kernel32.dll", SetLastError = true)]
	static extern bool TerminateProcess(IntPtr process, uint exitCode);

	[DllImport("advapi32.dll", SetLastError = true)]
	static extern bool OpenProcessToken(IntPtr process, uint access, out IntPtr token);

	[DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
	static extern bool LookupPrivilegeValue(string system, string name, out LUID luid);

	[DllImport("advapi32.dll", SetLastError = true)]
	static extern bool AdjustTokenPrivileges(IntPtr token, bool disableAll, ref TOKEN_PRIVILEGES newState, int length, IntPtr previous, IntPtr returned);

	[DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
	static extern bool ConvertStringSidToSid(string text, out IntPtr sid);

	[DllImport("advapi32.dll")]
	static extern uint LsaOpenPolicy(IntPtr system, ref LSA_OBJECT_ATTRIBUTES attributes, uint access, out IntPtr policy);

	[DllImport("advapi32.dll")]
	static extern uint LsaAddAccountRights(IntPtr policy, IntPtr sid, LSA_UNICODE_STRING[] rights, uint count);

	[DllImport("advapi32.dll")]
	static extern uint LsaRemoveAccountRights(IntPtr policy, IntPtr sid, bool allRights, LSA_UNICODE_STRING[] rights, uint count);

	[DllImport("advapi32.dll")]
	static extern uint LsaClose(IntPtr policy);

	[DllImport("advapi32.dll")]
	static extern uint LsaNtStatusToWinError(uint status);

	[DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
	static extern bool LogonUser(string user, string domain, IntPtr password, int logonType, int provider, out IntPtr token);

	[DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
	static extern bool CreateProcessAsUser(IntPtr token, string application, StringBuilder commandLine, IntPtr processAttributes, IntPtr threadAttributes, bool inheritHandles, uint flags, IntPtr environment, string directory, ref STARTUPINFO startup, out PROCESS_INFORMATION information);

	[DllImport("userenv.dll", SetLastError = true)]
	static extern bool CreateEnvironmentBlock(out IntPtr environment, IntPtr token, bool inherit);

	[DllImport("userenv.dll", SetLastError = true)]
	static extern bool DestroyEnvironmentBlock(IntPtr environment);

	[DllImport("userenv.dll", CharSet = CharSet.Unicode, SetLastError = true)]
	static extern bool LoadUserProfile(IntPtr token, ref PROFILEINFO profile);

	[DllImport("userenv.dll", SetLastError = true)]
	static extern bool UnloadUserProfile(IntPtr token, IntPtr profile);

	static void EnablePrivilege(string name) {
		IntPtr token;
		if (!OpenProcessToken(System.Diagnostics.Process.GetCurrentProcess().Handle, TOKEN_ADJUST_PRIVILEGES | TOKEN_QUERY, out token)) {
			throw new Win32Exception(Marshal.GetLastWin32Error());
		}
		try {
			LUID luid;
			if (!LookupPrivilegeValue(null, name, out luid)) {
				throw new Win32Exception(Marshal.GetLastWin32Error());
			}
			LUID_AND_ATTRIBUTES item = new LUID_AND_ATTRIBUTES();
			item.Luid = luid;
			item.Attributes = SE_PRIVILEGE_ENABLED;
			TOKEN_PRIVILEGES privileges = new TOKEN_PRIVILEGES();
			privileges.Count = 1;
			privileges.Priv = item;
			if (!AdjustTokenPrivileges(token, false, ref privileges, 0, IntPtr.Zero, IntPtr.Zero)) {
				throw new Win32Exception(Marshal.GetLastWin32Error());
			}
			int error = Marshal.GetLastWin32Error();
			if (error != 0) {
				throw new Win32Exception(error);
			}
		} finally {
			CloseHandle(token);
		}
	}

	static void TryEnable(string name) {
		try { EnablePrivilege(name); } catch (Win32Exception) { }
	}

	static void AccountRight(string sidText, bool add) {
		EnablePrivilege("SeTcbPrivilege");
		IntPtr sid;
		if (!ConvertStringSidToSid(sidText, out sid)) {
			throw new Win32Exception(Marshal.GetLastWin32Error());
		}
		IntPtr policy = IntPtr.Zero;
		try {
			LSA_OBJECT_ATTRIBUTES attributes = new LSA_OBJECT_ATTRIBUTES();
			attributes.Length = Marshal.SizeOf(typeof(LSA_OBJECT_ATTRIBUTES));
			uint status = LsaOpenPolicy(IntPtr.Zero, ref attributes, POLICY_LOOKUP_NAMES | POLICY_CREATE_ACCOUNT, out policy);
			if (status != 0) {
				throw new Win32Exception((int)LsaNtStatusToWinError(status));
			}
			string right = "SeBatchLogonRight";
			IntPtr buffer = Marshal.StringToHGlobalUni(right);
			try {
				LSA_UNICODE_STRING unicode = new LSA_UNICODE_STRING();
				unicode.Buffer = buffer;
				unicode.Length = (ushort)(right.Length * 2);
				unicode.MaximumLength = (ushort)((right.Length + 1) * 2);
				LSA_UNICODE_STRING[] rights = new LSA_UNICODE_STRING[] { unicode };
				if (add) {
					status = LsaAddAccountRights(policy, sid, rights, 1);
				} else {
					status = LsaRemoveAccountRights(policy, sid, false, rights, 1);
				}
				if (status != 0) {
					throw new Win32Exception((int)LsaNtStatusToWinError(status));
				}
			} finally {
				Marshal.FreeHGlobal(buffer);
			}
		} finally {
			if (policy != IntPtr.Zero) {
				LsaClose(policy);
			}
			LocalFree(sid);
		}
	}

	public static void GrantBatchLogon(string sidText) { AccountRight(sidText, true); }

	public static void RevokeBatchLogon(string sidText) { AccountRight(sidText, false); }

	public static FixtureProcess Start(string user, SecureString password, int logonType, string application, string commandLine, string directory) {
		TryEnable("SeAssignPrimaryTokenPrivilege");
		TryEnable("SeIncreaseQuotaPrivilege");
		TryEnable("SeBackupPrivilege");
		TryEnable("SeRestorePrivilege");
		IntPtr raw = Marshal.SecureStringToGlobalAllocUnicode(password);
		IntPtr token;
		try {
			if (!LogonUser(user, ".", raw, logonType, 0, out token)) {
				throw new Win32Exception(Marshal.GetLastWin32Error());
			}
		} finally {
			Marshal.ZeroFreeGlobalAllocUnicode(raw);
		}
		IntPtr profile = IntPtr.Zero;
		try {
			PROFILEINFO info = new PROFILEINFO();
			info.dwSize = Marshal.SizeOf(typeof(PROFILEINFO));
			info.dwFlags = 1;
			info.lpUserName = user;
			if (LoadUserProfile(token, ref info)) {
				profile = info.hProfile;
			}
			IntPtr environment = IntPtr.Zero;
			bool haveEnvironment = CreateEnvironmentBlock(out environment, token, false);
			try {
				STARTUPINFO startup = new STARTUPINFO();
				startup.cb = Marshal.SizeOf(typeof(STARTUPINFO));
				PROCESS_INFORMATION created;
				uint flags = 0x08000000;
				if (haveEnvironment) {
					flags |= 0x00000400;
				}
				StringBuilder command = new StringBuilder(commandLine, commandLine.Length + 1);
				if (!CreateProcessAsUser(token, application, command, IntPtr.Zero, IntPtr.Zero, false, flags, haveEnvironment ? environment : IntPtr.Zero, directory, ref startup, out created)) {
					throw new Win32Exception(Marshal.GetLastWin32Error());
				}
				CloseHandle(created.hThread);
				FixtureProcess process = new FixtureProcess(token, profile, created.hProcess);
				token = IntPtr.Zero;
				profile = IntPtr.Zero;
				return process;
			} finally {
				if (haveEnvironment && environment != IntPtr.Zero) {
					DestroyEnvironmentBlock(environment);
				}
			}
		} catch {
			if (profile != IntPtr.Zero) {
				UnloadUserProfile(token, profile);
			}
			if (token != IntPtr.Zero) {
				CloseHandle(token);
			}
			throw;
		}
	}
}

public sealed class FixtureProcess : IDisposable {
	IntPtr token;
	IntPtr profile;
	IntPtr process;

	public FixtureProcess(IntPtr token, IntPtr profile, IntPtr process) {
		this.token = token;
		this.profile = profile;
		this.process = process;
	}

	[DllImport("kernel32.dll", SetLastError = true)]
	static extern bool CloseHandle(IntPtr handle);

	[DllImport("kernel32.dll", SetLastError = true)]
	static extern uint WaitForSingleObject(IntPtr handle, uint milliseconds);

	[DllImport("kernel32.dll", SetLastError = true)]
	static extern bool GetExitCodeProcess(IntPtr process, out uint exitCode);

	[DllImport("kernel32.dll", SetLastError = true)]
	static extern bool TerminateProcess(IntPtr process, uint exitCode);

	[DllImport("userenv.dll", SetLastError = true)]
	static extern bool UnloadUserProfile(IntPtr token, IntPtr profile);

	public bool Wait(int milliseconds) {
		return WaitForSingleObject(process, (uint)milliseconds) == 0;
	}

	public int GetExitCode() {
		uint code;
		if (!GetExitCodeProcess(process, out code)) {
			throw new Win32Exception(Marshal.GetLastWin32Error());
		}
		return unchecked((int)code);
	}

	public void Kill() {
		TerminateProcess(process, 1);
	}

	public void Dispose() {
		if (process != IntPtr.Zero) {
			CloseHandle(process);
			process = IntPtr.Zero;
		}
		if (profile != IntPtr.Zero && token != IntPtr.Zero) {
			UnloadUserProfile(token, profile);
			profile = IntPtr.Zero;
		}
		if (token != IntPtr.Zero) {
			CloseHandle(token);
			token = IntPtr.Zero;
		}
	}
}
'@
}

function Add-Win32Code($Codes, $ErrorRecord) {
	$ex = $ErrorRecord.Exception
	while ($ex) {
		$win = $ex -as [ComponentModel.Win32Exception]
		if ($win) {
			[void]$Codes.Add([int]$win.NativeErrorCode)
			return
		}
		$match = [regex]::Match([string]$ex.Message, 'Win32 ([0-9]+)')
		if ($match.Success) {
			[void]$Codes.Add([int]$match.Groups[1].Value)
			return
		}
		$ex = $ex.InnerException
	}
}

function Format-FixtureStartNote($Codes) {
	$seen = @{}
	$parts = New-Object System.Collections.Generic.List[string]
	foreach ($code in @($Codes)) {
		$n = [int]$code
		if ($n -le 0 -or $seen.ContainsKey([string]$n)) { continue }
		$seen[[string]$n] = $true
		[void]$parts.Add('Win32 ' + $n)
	}
	if ($parts.Count -eq 0) { return 'could not start msiexec as fixture user' }
	return 'could not start msiexec as fixture user: ' + ($parts -join ' ')
}

function Enable-SecondaryLogon {
	$key = 'HKLM:\SYSTEM\CurrentControlSet\Services\seclogon'
	if (-not (Test-Path -LiteralPath $key)) { throw 'could not start msiexec as fixture user: Win32 1060' }
	$start = [int](Get-ItemProperty -LiteralPath $key).Start
	$running = (Get-Service -Name seclogon).Status -eq 'Running'
	$script:SecLogonPriorStart = $start
	$script:SecLogonPriorRunning = $running
	if ($start -eq 4) {
		& sc.exe config seclogon start= demand | Out-Null
		if ($LASTEXITCODE -ne 0) { throw "could not start msiexec as fixture user: Win32 $LASTEXITCODE" }
	}
	if (-not $running) {
		& sc.exe start seclogon | Out-Null
		$code = [int]$LASTEXITCODE
		if ($code -ne 0 -and $code -ne 1056) { throw "could not start msiexec as fixture user: Win32 $code" }
		$deadline = (Get-Date).AddSeconds(30)
		do {
			if ((Get-Service -Name seclogon).Status -eq 'Running') { return }
			if ((Get-Date) -ge $deadline) { throw 'could not start msiexec as fixture user: Win32 1053' }
			Start-Sleep -Seconds 1
		} while ($true)
	}
}

function Restore-SecondaryLogon {
	if ($null -eq $script:SecLogonPriorStart) { return }
	$key = 'HKLM:\SYSTEM\CurrentControlSet\Services\seclogon'
	try {
		if (Test-Path -LiteralPath $key) {
			$prior = [int]$script:SecLogonPriorStart
			$current = [int](Get-ItemProperty -LiteralPath $key).Start
			if ($current -ne $prior) {
				$word = 'demand'
				if ($prior -eq 2) { $word = 'auto' }
				elseif ($prior -eq 4) { $word = 'disabled' }
				& sc.exe config seclogon start= $word | Out-Null
			}
			if (-not $script:SecLogonPriorRunning) {
				& sc.exe stop seclogon | Out-Null
			}
		}
	} catch {
		# The case result does not depend on restoring this service.
	} finally {
		$script:SecLogonPriorStart = $null
	}
}

function Start-TokenMsiexec([string]$User, [Security.SecureString]$Password, [string]$Arguments, [int]$TimeoutSec, [int]$LogonType) {
	$exe = Join-Path $env:SystemRoot 'System32\msiexec.exe'
	$command = '"' + $exe + '" ' + $Arguments
	$proc = [WinunitdFixture]::Start($User, $Password, $LogonType, $exe, $command, $env:SystemRoot)
	try {
		if (-not $proc.Wait($TimeoutSec * 1000)) {
			try { $proc.Kill() } catch { }
			throw 'msiexec timed out'
		}
		return $proc.GetExitCode()
	} finally {
		$proc.Dispose()
	}
}

function Start-LogonMsiexec([string]$User, [Security.SecureString]$Password, [string]$Arguments, [int]$TimeoutSec) {
	$info = New-MsiexecStartInfo $Arguments $false $true $User $Password $true
	try {
		$proc = [System.Diagnostics.Process]::Start($info)
	} catch {
		$first = $_
		$info = New-MsiexecStartInfo $Arguments $false $true $User $Password $false
		try {
			$proc = [System.Diagnostics.Process]::Start($info)
		} catch {
			throw $first
		}
	}
	if (-not $proc.WaitForExit($TimeoutSec * 1000)) {
		try { $proc.Kill() } catch { }
		throw 'msiexec timed out'
	}
	$proc.Refresh()
	return [int]$proc.ExitCode
}

function ConvertTo-PlainPassword([Security.SecureString]$Password) {
	$ptr = [Runtime.InteropServices.Marshal]::SecureStringToGlobalAllocUnicode($Password)
	try { return [Runtime.InteropServices.Marshal]::PtrToStringUni($ptr) }
	finally { [Runtime.InteropServices.Marshal]::ZeroFreeGlobalAllocUnicode($ptr) }
}

function Start-TaskMsiexec([string]$User, [Security.SecureString]$Password, [string]$Arguments, [int]$TimeoutSec) {
	$name = 'winunitd-acceptance-alice'
	$service = New-Object -ComObject 'Schedule.Service'
	$service.Connect()
	$folder = $service.GetFolder('\')
	try { $folder.DeleteTask($name, 0) } catch { }
	$task = $service.NewTask(0)
	$task.Principal.UserId = '.\' + $User
	$task.Principal.LogonType = 1
	$task.Principal.RunLevel = 0
	$task.Settings.DisallowStartIfOnBatteries = $false
	$task.Settings.StopIfGoingOnBatteries = $false
	$task.Settings.ExecutionTimeLimit = 'PT4H'
	$action = $task.Actions.Create(0)
	$action.Path = Join-Path $env:SystemRoot 'System32\msiexec.exe'
	$action.Arguments = $Arguments
	$plain = ConvertTo-PlainPassword $Password
	try {
		try {
			$registered = $folder.RegisterTaskDefinition($name, $task, 6, ('.\' + $User), $plain, 1, $null)
		} catch {
			$win = 0
			if ($_.Exception -and $_.Exception.HResult) { $win = [int]($_.Exception.HResult -band 0xFFFF) }
			if ($win -le 0) { $win = 1385 }
			throw "could not start msiexec as fixture user: Win32 $win"
		}
	} finally {
		$plain = $null
	}
	try {
		$beforeRun = [datetime]$registered.LastRunTime
		$registered.Run($null) | Out-Null
		$deadline = (Get-Date).AddSeconds($TimeoutSec)
		$sawRunning = $false
		$code = 0
		while ($true) {
			$current = $folder.GetTask($name)
			$state = [int]$current.State
			$code = [int64]$current.LastTaskResult
			$ran = ([datetime]$current.LastRunTime) -gt $beforeRun
			if ($state -eq 4) { $sawRunning = $true }
			$busy = $state -eq 4 -or $state -eq 2 -or $code -eq 267009 -or $code -eq 267011
			if (($sawRunning -or $ran) -and -not $busy) { break }
			if ((Get-Date) -ge $deadline) {
				try { $current.Stop(0) } catch { }
				throw 'msiexec timed out'
			}
			Start-Sleep -Seconds 1
		}
		if (($code -band 0x80000000) -ne 0) {
			$win = [int]($code -band 0xFFFF)
			if ($win -le 0) { $win = [int]$code }
			throw "could not start msiexec as fixture user: Win32 $win"
		}
		return $code
	} finally {
		try { $folder.DeleteTask($name, 0) } catch { }
	}
}

function Start-FixtureMsiexec([string]$User, [Security.SecureString]$Password, [string]$Arguments, [int]$TimeoutSec) {
	Ensure-FixtureType
	$codes = New-Object System.Collections.Generic.List[int]
	$exit = 0
	$started = $false
	try {
		try { Enable-SecondaryLogon } catch { Add-Win32Code $codes $_ }
		$sidText = ([Security.Principal.NTAccount]::new($User)).Translate([Security.Principal.SecurityIdentifier]).Value
		foreach ($logonType in @(4, 2)) {
			if ($started) { break }
			$granted = $false
			try {
				if ($logonType -eq 4) {
					[WinunitdFixture]::GrantBatchLogon($sidText)
					$granted = $true
				}
				$exit = Start-TokenMsiexec -User $User -Password $Password -Arguments $Arguments -TimeoutSec $TimeoutSec -LogonType $logonType
				$started = $true
			} catch {
				if ($_.Exception.Message -eq 'msiexec timed out') { throw }
				Add-Win32Code $codes $_
			} finally {
				if ($granted) {
					try { [WinunitdFixture]::RevokeBatchLogon($sidText) } catch { }
				}
			}
		}
		if (-not $started) {
			try {
				$exit = Start-LogonMsiexec -User $User -Password $Password -Arguments $Arguments -TimeoutSec $TimeoutSec
				$started = $true
			} catch {
				if ($_.Exception.Message -eq 'msiexec timed out') { throw }
				Add-Win32Code $codes $_
			}
		}
		if (-not $started) {
			try {
				$exit = Start-TaskMsiexec -User $User -Password $Password -Arguments $Arguments -TimeoutSec $TimeoutSec
				$started = $true
			} catch {
				if ($_.Exception.Message -eq 'msiexec timed out') { throw }
				Add-Win32Code $codes $_
			}
		}
		if (-not $started) { throw (Format-FixtureStartNote $codes) }
		return $exit
	} finally {
		Restore-SecondaryLogon
	}
}

function Invoke-NonAdmin {
	$before = Get-ServiceState
	$created = $false
	$script:FixtureCleanupNote = ''
	$result = $null
	$grants = New-Object System.Collections.Generic.List[object]
	try {
		if ($script:Identity -eq 'SYSTEM' -or $script:Identity -eq 'Administrator') {
			try {
				Import-Module Microsoft.PowerShell.LocalAccounts -ErrorAction Stop
				if (Get-LocalUser -Name 'alice' -ErrorAction SilentlyContinue) {
					$result = New-CaseResult 'failed' 'fixture user alice already exists' $null '' $false $before $before $null
				} else {
					$plain = New-FixturePassword
					$secure = ConvertTo-SecureString $plain -AsPlainText -Force
					New-LocalUser -Name 'alice' -Password $secure -PasswordNeverExpires -UserMayNotChangePassword -AccountNeverExpires -Description 'acceptance fixture' | Out-Null
					$created = $true
					$plain = ''
					$sid = ([Security.Principal.NTAccount]::new('alice')).Translate([Security.Principal.SecurityIdentifier])
					if (Test-FixtureUserElevated $sid) {
						$result = New-CaseResult 'failed' 'fixture user is elevated' $null '' $false $before $before $null
					} else {
						$msiDir = Split-Path -Parent $MsiPath
						[void]$grants.Add([pscustomobject]@{ Path = $EvidenceDirectory; Rule = (Add-AccessRule $EvidenceDirectory $sid 'Modify' $true) })
						[void]$grants.Add([pscustomobject]@{ Path = $msiDir; Rule = (Add-AccessRule $msiDir $sid 'ReadAndExecute' $false) })
						[void]$grants.Add([pscustomobject]@{ Path = $MsiPath; Rule = (Add-AccessRule $MsiPath $sid 'ReadAndExecute' $false) })
						$script:SummaryIdentity = 'standard-user'
						$run = Invoke-Msiexec -LogName 'non-admin' -Quiet -TimeoutSec 180 -RunAs 'alice' -RunAsPassword $secure -Words @('/i', (ConvertTo-MsiArg $MsiPath))
						$result = Invoke-NonAdminResult $run $before
					}
				}
			} catch {
				$failure = $_
				$errPath = Join-Path $EvidenceDirectory 'non-admin.err'
				try {
					$text = $failure | Out-String
					if ($text -match 'Aa1!') { $text = [string]$failure.Exception.Message }
					if ($text -match 'Aa1!') { $text = 'could not start msiexec as fixture user' }
					$text | Out-File -FilePath $errPath -Encoding utf8
				} catch { }
				if (-not $result) {
					$note = 'could not create fixture user'
					$message = [string]$failure.Exception.Message
					if ($message -eq 'msiexec timed out') { $note = 'msiexec timed out' }
					elseif ($message -match '^could not start msiexec as fixture user(?:: Win32 [0-9]+(?: Win32 [0-9]+)*)?$') { $note = $message }
					elseif ($created) { $note = 'could not start msiexec as fixture user' }
					$result = New-CaseResult 'failed' $note $null '' $false $before (Get-ServiceState) $null
				}
			}
		} else {
			$run = Invoke-Msiexec -LogName 'non-admin' -Quiet -TimeoutSec 180 -Words @('/i', (ConvertTo-MsiArg $MsiPath))
			$result = Invoke-NonAdminResult $run $before
		}
	} finally {
		foreach ($grant in $grants) {
			try { Remove-AccessRule $grant.Path $grant.Rule } catch { $script:FixtureCleanupNote = 'fixture access cleanup failed' }
		}
		if ($created) {
			try {
				Import-Module Microsoft.PowerShell.LocalAccounts -ErrorAction Stop
				Remove-LocalUser -Name 'alice' | Out-Null
			} catch {
				$script:FixtureCleanupNote = 'fixture user cleanup failed'
			}
		}
	}
	if (-not $result) { $result = New-CaseResult 'failed' 'could not create fixture user' $null '' $false $before (Get-ServiceState) $null }
	if ($script:FixtureCleanupNote) {
		$result.Status = 'failed'
		if (-not $result.Note) { $result.Note = $script:FixtureCleanupNote }
		else { $result.Note = $result.Note + '; ' + $script:FixtureCleanupNote }
	}
	return $result
}

function Stop-AcceptanceService {
	$state = Get-ServiceState
	if ($state -eq 'absent' -or $state -eq 'stopped') { return $true }
	Stop-Service -Name winunitd -Force -ErrorAction SilentlyContinue
	return Wait-ServiceState 'stopped' 180
}

function Invoke-BetaConflict {
	$upgrade = ConvertTo-GuidText (Get-MsiProperty $BetaMsi 'UpgradeCode')
	if ($upgrade -ne $BetaUpgradeCode) { return New-CaseResult 'failed' 'beta MSI upgrade identity mismatch' $null '' $false (Get-ServiceState) (Get-ServiceState) $null }
	$before = Get-ServiceState
	$beta = Invoke-Msiexec -LogName 'beta-conflict-beta' -Quiet -TimeoutSec 600 -Words @('/i', (ConvertTo-MsiArg $BetaMsi))
	if ($beta.ExitCode -ne 0 -or $beta.Reboot) { return New-CaseResult 'failed' 'beta package install failed' $beta.ExitCode $beta.Log $beta.Reboot $before (Get-ServiceState) $null }
	if (Test-Path -LiteralPath (Get-ProductExe)) { return New-CaseResult 'failed' 'product layout appeared' $beta.ExitCode $beta.Log $beta.Reboot $before (Get-ServiceState) $null }
	if (-not (Test-Path -LiteralPath (Get-BetaExe))) { return New-CaseResult 'failed' 'beta package layout mismatch' $beta.ExitCode $beta.Log $beta.Reboot $before (Get-ServiceState) $null }
	$run = Invoke-Msiexec -LogName 'beta-conflict' -Quiet -TimeoutSec 600 -Words @('/i', (ConvertTo-MsiArg $MsiPath))
	$note = ''
	if ($run.Reboot) { $note = 'reboot started' }
	elseif ($run.ExitCode -ne 1603) { $note = 'unexpected exit' }
	elseif (Test-Path -LiteralPath (Get-ProductExe)) { $note = 'product layout appeared' }
	elseif (-not (Test-Path -LiteralPath (Get-BetaExe))) { $note = 'beta package was replaced' }
	# The beta package rejects uninstall while winunitd is running.
	# CheckStopped and PreparePolicy both run on REMOVE before StopServices.
	$stopped = Stop-AcceptanceService
	$remove = Invoke-Msiexec -LogName 'beta-conflict-remove' -Quiet -TimeoutSec 600 -Words @('/x', (ConvertTo-MsiArg $BetaMsi))
	$removeFailed = $remove.Reboot -or ($null -eq $remove.ExitCode) -or ($remove.ExitCode -ne 0)
	$lingering = (Get-ServiceState) -eq 'running' -or (Get-ServiceState) -eq 'other'
	if ((-not $stopped) -or $removeFailed -or $lingering) {
		Stop-AcceptanceService | Out-Null
		$lingering = (Get-ServiceState) -eq 'running' -or (Get-ServiceState) -eq 'other'
		$cleanup = 'beta cleanup failed: remove exit ' + [string]$remove.ExitCode
		if (-not $stopped) { $cleanup = 'beta cleanup failed: service did not stop; remove exit ' + [string]$remove.ExitCode }
		elseif ($lingering) { $cleanup = 'beta cleanup failed: service still running; remove exit ' + [string]$remove.ExitCode }
		if (-not $note) { $note = $cleanup }
		elseif ($note -notlike '*remove exit*') { $note = $note + '; ' + $cleanup }
	}
	return New-CaseResult $(if ($note) { 'failed' } else { 'passed' }) $note $run.ExitCode $run.Log $run.Reboot $before (Get-ServiceState) $null
}

function Invoke-PreflightReparse {
	$link = Get-DataRoot
	if (Test-Path -LiteralPath $link) { return New-CaseResult 'not_run' 'data directory already present' $null '' $false (Get-ServiceState) (Get-ServiceState) $null }
	$target = Join-Path ([IO.Path]::GetTempPath()) 'winunitd-acceptance-reparse'
	$marker = Join-Path $target 'marker.txt'
	$created = $false
	try {
		if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Recurse -Force }
		New-Item -ItemType Directory -Path $target | Out-Null
		Set-Content -LiteralPath $marker -Value 'keep' -Encoding ascii
		& cmd.exe /c ('mklink /J "{0}" "{1}"' -f $link, $target) | Out-Null
		if ($LASTEXITCODE -ne 0) { throw 'could not create junction' }
		$item = Get-Item -LiteralPath $link -Force
		if (-not ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'could not create junction' }
		$created = $true
		$before = Get-ServiceState
		$run = Invoke-Msiexec -LogName 'preflight-reparse' -Quiet -TimeoutSec 600 -Words @('/i', (ConvertTo-MsiArg $MsiPath))
		$note = ''
		$kept = $false
		if ($run.Reboot) { $note = 'reboot started' }
		elseif ($run.ExitCode -ne 1603) { $note = 'unexpected exit' }
		else {
			$after = Get-Item -LiteralPath $link -Force -ErrorAction SilentlyContinue
			if (-not $after -or -not ($after.Attributes -band [IO.FileAttributes]::ReparsePoint)) { $note = 'reparse point was replaced' }
			elseif (([IO.File]::ReadAllText($marker)).Trim() -ne 'keep') { $note = 'reparse target changed' }
			elseif (Test-Path -LiteralPath (Get-ProductExe)) { $note = 'product layout appeared' }
			else { $kept = $true }
		}
		return New-CaseResult $(if ($note) { 'failed' } else { 'passed' }) $note $run.ExitCode $run.Log $run.Reboot $before (Get-ServiceState) $kept
	} finally {
		if ($created -and (Test-Path -LiteralPath $link)) {
			$item = Get-Item -LiteralPath $link -Force
			if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
				& cmd.exe /c ('rmdir "{0}"' -f $link) | Out-Null
			}
		}
		if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Recurse -Force }
	}
}

function Invoke-PreflightUnmanaged {
	if (Get-Service -Name winunitd -ErrorAction SilentlyContinue) {
		return New-CaseResult 'not_run' 'winunitd service already exists' $null '' $false (Get-ServiceState) (Get-ServiceState) $null
	}
	$image = Join-Path $env:SystemRoot 'System32\cmd.exe'
	$created = $false
	try {
		& cmd.exe /c ('sc.exe create winunitd binPath= "{0}" start= demand' -f $image) | Out-Null
		if ($LASTEXITCODE -ne 0) { throw 'could not create fixture service' }
		$created = $true
		$before = Get-ServiceState
		$run = Invoke-Msiexec -LogName 'preflight-unmanaged-service' -Quiet -TimeoutSec 600 -Words @('/i', (ConvertTo-MsiArg $MsiPath))
		$note = ''
		if ($run.Reboot) { $note = 'reboot started' }
		elseif ($run.ExitCode -ne 1603) { $note = 'unexpected exit' }
		elseif (Test-Path -LiteralPath (Get-ProductExe)) { $note = 'product layout appeared' }
		else {
			$svc = Get-CimInstance Win32_Service -Filter "Name='winunitd'"
			if (-not $svc -or [string]$svc.PathName -notlike '*\cmd.exe*') { $note = 'service image changed' }
		}
		return New-CaseResult $(if ($note) { 'failed' } else { 'passed' }) $note $run.ExitCode $run.Log $run.Reboot $before (Get-ServiceState) $null
	} finally {
		if ($created) { & cmd.exe /c 'sc.exe delete winunitd' | Out-Null }
	}
}

function Resolve-InputPath([string]$Path, [string]$MissingNote) {
	if (-not $Path -or -not (Test-Path -LiteralPath $Path)) { throw $MissingNote }
	return (Resolve-Path -LiteralPath $Path).Path
}

$matrixPath = Join-Path $PSScriptRoot 'acceptance-matrix.json'
$matrix = Get-Content -LiteralPath $matrixPath -Raw -Encoding utf8 | ConvertFrom-Json
if ($Case -eq 'list') {
	foreach ($item in @(ConvertTo-Array $matrix.cases)) { Write-Output $item.id }
	exit 0
}
if ($env:OS -ne 'Windows_NT') { throw 'Windows guest required' }
if (-not [Environment]::Is64BitProcess) { throw '64-bit Windows PowerShell is required' }
if (-not $DisposableGuest) { throw 'Disposable guest acknowledgement required' }
if ($EvidenceId -and $EvidenceId -notmatch '^[a-z0-9][a-z0-9-]{0,63}$') { throw 'evidence id must be a short public token' }
if (-not $EvidenceDirectory) { throw 'Evidence directory is required' }
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$evidenceFull = [IO.Path]::GetFullPath($EvidenceDirectory)
$repoPrefix = $repoRoot.TrimEnd('\') + '\'
if ($evidenceFull.TrimEnd('\') -eq $repoRoot.TrimEnd('\') -or $evidenceFull.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase)) {
	throw 'Evidence directory must stay outside the repository'
}
$driveRoot = [IO.Path]::GetPathRoot($evidenceFull)
if ($evidenceFull.TrimEnd('\') -eq $driveRoot.TrimEnd('\')) { throw 'Evidence directory must be a subdirectory' }
New-Item -ItemType Directory -Force -Path $evidenceFull | Out-Null
$EvidenceDirectory = $evidenceFull

try {
	$MsiPath = Resolve-InputPath $MsiPath 'product MSI was not found'
	$spec = $null
	foreach ($item in @(ConvertTo-Array $matrix.cases)) {
		if ($item.id -eq $Case) { $spec = $item }
	}
	if (-not $spec) { throw 'unknown case' }
	if ($OlderMsi) { $OlderMsi = Resolve-InputPath $OlderMsi 'older MSI was not found' }
	elseif ($spec.requires.older_msi) {
		try { $OlderMsi = Find-OlderProductMsi $repoRoot $MsiPath } catch {
			if ($_.Exception.Message -eq 'multiple packages share the selected version') {
				$script:Guest = Get-GuestFacts
				$script:GuestSku = Get-ClaimedSku $script:Guest.Product $script:Guest.Edition $script:Guest.Build $script:Guest.InstallationType
				$script:Identity = Get-IdentityLabel
				$result = New-CaseResult 'not_run' 'multiple older packages' $null '' $false (Get-ServiceState) (Get-ServiceState) $null
				Write-Summary $result
				exit 2
			}
			throw
		}
	}
	if ($BetaMsi) { $BetaMsi = Resolve-InputPath $BetaMsi 'beta MSI was not found' }
	elseif ($spec.requires.beta_msi) {
		try { $BetaMsi = Find-BetaMsi $repoRoot $MsiPath } catch {
			if ($_.Exception.Message -eq 'multiple packages share the selected version') {
				$script:Guest = Get-GuestFacts
				$script:GuestSku = Get-ClaimedSku $script:Guest.Product $script:Guest.Edition $script:Guest.Build $script:Guest.InstallationType
				$script:Identity = Get-IdentityLabel
				$result = New-CaseResult 'not_run' 'multiple beta packages' $null '' $false (Get-ServiceState) (Get-ServiceState) $null
				Write-Summary $result
				exit 2
			}
			throw
		}
	}
	$script:Guest = Get-GuestFacts
	$script:GuestSku = Get-ClaimedSku $script:Guest.Product $script:Guest.Edition $script:Guest.Build $script:Guest.InstallationType
	$script:Identity = Get-IdentityLabel
	$script:Interactive = Test-InteractiveDesktop
	$script:Offline = Test-OfflineGuest
	Assert-PackageIdentity
	$script:Commit = Get-RecordedCommit
	$skip = Get-SkipReason $spec
	if ($skip) {
		$status = 'not_run'
		$note = $skip
		if ($skip.StartsWith('NOT_APPLICABLE:')) {
			$status = 'not_applicable'
			$note = $skip.Substring(15)
		}
		$result = New-CaseResult $status $note $null '' $false (Get-ServiceState) (Get-ServiceState) $null
		Write-Summary $result
		exit 2
	}
	if (-not $script:Commit) { throw 'source commit is required' }
	Remove-Item Env:WINUNITD_TEST_FAIL -ErrorAction SilentlyContinue
	$result = Invoke-SelectedCase
	if ($script:FixturesWritten -and $result.Status -ne 'passed') { Clear-Fixtures | Out-Null }
	if ($result.Reboot -and $result.Status -eq 'passed') {
		$result.Status = 'failed'
		$result.Note = 'reboot started'
	}
	if ($result.Status -eq 'passed') {
		$allowed = @(ConvertTo-Array $spec.expect_exit)
		if ($allowed -notcontains $result.Exit) {
			$result.Status = 'failed'
			$result.Note = 'unexpected exit'
		}
		$wantMarkers = @(ConvertTo-Array $spec.expect_markers)
		$gotMarkers = @(Get-LogMarkers $result.Log)
		foreach ($marker in $wantMarkers) {
			if (-not $marker) { continue }
			if ($gotMarkers -notcontains $marker) {
				$result.Status = 'failed'
				if (-not $result.Note) { $result.Note = 'log marker missing' }
			}
		}
	}
	Write-Summary $result
	if ($result.Status -eq 'passed') { exit 0 }
	if ($result.Status -eq 'not_run' -or $result.Status -eq 'not_applicable') { exit 2 }
	exit 1
} catch {
	$errPath = Join-Path $EvidenceDirectory ($Case + '.err')
	$_ | Out-File -FilePath $errPath -Encoding utf8
	if ($script:FixturesWritten) { Clear-Fixtures | Out-Null }
	try {
		if ($script:Guest) {
			$failed = New-CaseResult 'failed' 'case failed' $null '' $false (Get-ServiceState) (Get-ServiceState) $null
			Write-Summary $failed
		}
	} catch {
		# The private error file retains the original failure.
	}
	Write-Output "$Case failed"
	exit 1
}
