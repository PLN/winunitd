$ErrorActionPreference = 'Stop'
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'Expected SYSTEM' }
if (Get-Service winunitd -ErrorAction SilentlyContinue) { throw 'Fresh guest required: service already exists' }
if (Get-NetRoute -DestinationPrefix @('0.0.0.0/0', '::/0') -ErrorAction SilentlyContinue) { throw 'Disconnect maintenance routing before qualification' }
Set-Location C:\winunitd-lab
Start-Transcript C:\winunitd-lab\smoke-before-reboot.log
Get-FileHash .\winunitd.exe, .\winctl.exe
New-Item -ItemType Directory -Force C:\winunitd-lab\data\units | Out-Null
'while ($true) { Write-Output "winunitd lab heartbeat"; Start-Sleep -Seconds 5 }' | Set-Content C:\winunitd-lab\fixture.ps1
@"
[Unit]
Description=Disposable VM smoke fixture
[Service]
Type=simple
ExecStart=C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe
ExecStartArg=-NoProfile
ExecStartArg=-File
ExecStartArg=C:\winunitd-lab\fixture.ps1
WorkingDirectory=C:\winunitd-lab
Restart=on-failure
[Install]
WantedBy=default.target
"@ | Set-Content C:\winunitd-lab\data\units\smoke.service
& .\winctl.exe verify C:\winunitd-lab\data\units\smoke.service
if ($LASTEXITCODE) { throw 'Unit verification failed' }
& .\winunitd.exe install --base-dir C:\winunitd-lab\data
if ($LASTEXITCODE) { throw 'Service install failed' }
Start-Service winunitd
$deadline = (Get-Date).AddSeconds(45)
do { & .\winctl.exe list-units; if ($LASTEXITCODE -eq 0) { break }; Start-Sleep 1 } while ((Get-Date) -lt $deadline)
if ($LASTEXITCODE) { throw 'Manager did not become ready' }
& .\winctl.exe enable smoke.service
if ($LASTEXITCODE) { throw 'Enable failed' }
& .\winctl.exe start smoke.service
if ($LASTEXITCODE) { throw 'Start failed' }
Start-Sleep 6
& .\winctl.exe status smoke.service
if ($LASTEXITCODE) { throw 'Fixture not active' }
& .\winctl.exe logs smoke.service
& .\winctl.exe stop smoke.service
if ($LASTEXITCODE) { throw 'Stop failed' }
& .\winctl.exe status smoke.service
if ($LASTEXITCODE -ne 3) { throw 'Fixture not inactive' }
if (Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*-File C:\winunitd-lab\fixture.ps1*' }) { throw 'Fixture process survived stop' }
& .\winctl.exe start smoke.service
if ($LASTEXITCODE) { throw 'Second start failed' }
Get-CimInstance Win32_Service -Filter "Name='winunitd'" | Select-Object Name, State, StartName, StartMode
Get-CimInstance Win32_OperatingSystem | Select-Object Caption, Version, LastBootUpTime
& .\winctl.exe status smoke.service | Set-Content C:\winunitd-lab\status-before-reboot.txt
(Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToString('o') | Set-Content C:\winunitd-lab\boot-before-reboot.txt
'PASS before reboot' | Set-Content C:\winunitd-lab\smoke-before-reboot.result
Stop-Transcript
