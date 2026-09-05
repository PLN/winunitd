param([switch]$DisposableLab)
$ErrorActionPreference = 'Stop'
if (!$DisposableLab -or [Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') {
	throw 'Requires explicit disposable-lab opt-in and SYSTEM'
}
if (Get-NetRoute -ErrorAction SilentlyContinue | Where-Object { $_.DestinationPrefix -in @('0.0.0.0/0', '::/0') }) {
	throw 'Offline guest required'
}
$base = 'C:\winunitd-journal-pressure'
if (Test-Path $base) { throw 'Fresh fixture directory required' }
if (Get-Volume -DriveLetter Z -ErrorAction SilentlyContinue) { throw 'Fixture drive letter is occupied' }
$media = @(Get-Volume | Where-Object { $_.DriveType -eq 'CD-ROM' -and $_.DriveLetter } | ForEach-Object {
	$path = "$($_.DriveLetter):\journal.test.exe"
	if (Test-Path $path) { $path }
})
if ($media.Count -ne 1) { throw 'Expected exactly one test binary on fixture media' }
New-Item -ItemType Directory $base | Out-Null
Copy-Item -LiteralPath $media[0] -Destination "$base\journal.test.exe"
$vhd = "$base\pressure.vhdx"
$create = @"
create vdisk file="$vhd" maximum=64 type=expandable
select vdisk file="$vhd"
attach vdisk
create partition primary
format fs=ntfs quick label=winunitd-test
assign letter=Z
"@
$create | Set-Content -Encoding ASCII "$base\create.txt"
try {
	& diskpart.exe /s "$base\create.txt" | Out-File "$base\diskpart-create.log"
	if ($LASTEXITCODE) { throw 'Disposable VHD preparation failed' }
	$env:WINUNITD_TEST_JOURNAL_VOLUME = 'Z:\'
	& "$base\journal.test.exe" '-test.run=^TestDisposableVolumeDiskFullRecovery$' '-test.v' '-test.timeout=90s' *> "$base\test.log"
	$testExit = $LASTEXITCODE
	if ($testExit) { throw "Disk-pressure test failed: $testExit" }
	$os = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
	@{
		schema = 1
		passed = $true
		build = "$($os.CurrentBuild).$($os.UBR)"
		test_sha256 = (Get-FileHash "$base\journal.test.exe" -Algorithm SHA256).Hash.ToLowerInvariant()
		finished_utc = [DateTime]::UtcNow.ToString('o')
	} | ConvertTo-Json | Set-Content -Encoding UTF8 "$base\result.json"
} finally {
	Remove-Item Env:\WINUNITD_TEST_JOURNAL_VOLUME -ErrorAction SilentlyContinue
	@("select vdisk file=`"$vhd`"", 'detach vdisk') | Set-Content -Encoding ASCII "$base\detach.txt"
	& diskpart.exe /s "$base\detach.txt" | Out-File "$base\diskpart-detach.log"
	if ($LASTEXITCODE -or (Get-Volume -DriveLetter Z -ErrorAction SilentlyContinue)) {
		throw 'Fixture VHD detachment needs inspection'
	}
}
