param(
    [Parameter(Mandatory=$true)][string]$Config,
    [ValidateSet('Plan','Status','Rehearse','FailAfterStop','Update','Recover')][string]$Action='Plan',
    [ValidateRange(0,120)][int]$DelaySeconds=60,
    [switch]$Worker,
    [switch]$Library
)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
$unitRequestAction=$Action;$unitRequestDelay=$DelaySeconds;$unitWorker=$Worker;$unitLibrary=$Library
. (Join-Path $PSScriptRoot 'hermes-pilot.ps1') -Config $Config -Library
$Action=$unitRequestAction;$DelaySeconds=$unitRequestDelay;$Worker=$unitWorker;$Library=$unitLibrary

function Assert-UnitMaintenanceConfig($c) {
    Assert-Config $c
    foreach ($name in @($c.TargetUnit,$c.MaintenanceUnit)) {
        if ($name -notmatch '^[a-zA-Z0-9_-]+\.(service|target)$') { throw 'Invalid maintenance unit identity' }
    }
    if ($c.TargetUnit -notlike '*.target' -or $c.MaintenanceUnit -notlike '*.service' -or $c.MaintenanceUnit -in $c.Units) { throw 'Maintenance must be separate from workload units' }
    if ($c.Python.StartsWith($c.HermesHome, [StringComparison]::OrdinalIgnoreCase)) { throw 'Updater interpreter must be outside the mutable Hermes home' }
}
function Get-UnitManager($c) {
    $managers=@(Get-CimInstance Win32_Process | Where-Object ExecutablePath -eq (Join-Path $c.PilotRoot 'bin\winunitd.exe'))
    if ($managers.Count -ne 1) { throw 'Expected one user manager' }
    return $managers[0]
}
function Assert-UnitWorker($c) {
    $manager=Get-UnitManager $c
    $current=Get-CimInstance Win32_Process -Filter "ProcessId=$PID"
    if ($current.ParentProcessId -ne $manager.ProcessId) { throw 'Worker must be launched directly by its winunitd unit' }
    $identity=[Security.Principal.WindowsIdentity]::GetCurrent()
    $principal=New-Object Security.Principal.WindowsPrincipal($identity)
    if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Run the user-maintenance unit without elevation' }
}
function Invoke-UnitControl($c,[string[]]$Arguments,$run,$label) {
    $code=Invoke-Native (Join-Path $c.PilotRoot 'bin\winctl.exe') (@('--user')+$Arguments) (Join-Path $run ($label+'.log')) 180
    if ($code -ne 0) { throw "winctl $($Arguments -join ' ') failed ($code); inspect $label.log" }
}
function Read-UnitList($c) {
    $listing=(& (Join-Path $c.PilotRoot 'bin\winctl.exe') --user list-units 2>&1) -join "`n"
    if ($LASTEXITCODE -ne 0) { throw 'User manager control unavailable' }
    return $listing
}
function Save-UnitSnapshot($c,$run) {
    Assert-NoAutostart $c
    $unitDir=Join-Path $c.PilotRoot 'data\units'
    $maintenanceText=Get-Content -LiteralPath (Join-Path $unitDir $c.MaintenanceUnit) -Raw
    if ($maintenanceText -match '(?im)^\s*(PartOf|Requires|Wants|BindsTo|After|Before|WantedBy)\s*=') { throw 'Maintenance must have no workload or boot dependencies' }
    foreach ($link in @(Get-ChildItem -LiteralPath (Join-Path $c.PilotRoot 'data\enabled') -Recurse -File)) {
        if ($link.Name -in $c.Units -and $link.Directory.Name -ne $c.TargetUnit) { throw 'A workload has an enable link outside the maintenance target' }
        if ($link.Name -eq $c.MaintenanceUnit) { throw 'Maintenance must not be enabled at boot' }
    }
    $manager=Get-UnitManager $c
    $listing=Read-UnitList $c
    foreach ($name in @($c.TargetUnit)+$c.Units) {
        if ($listing -notmatch ([regex]::Escape($name)+'\s+loaded\s+active\s')) { throw "Workload is not active: $name" }
    }
    $enabled=$listing -match ([regex]::Escape($c.TargetUnit)+'\s+loaded\s+active\s+yes\s')
    Write-Json (Join-Path $run 'baseline.json') @{
        managerPid=$manager.ProcessId;managerCreated=[string]$manager.CreationDate;targetEnabled=$enabled
        binaries=@(Get-ChildItem -LiteralPath (Join-Path $c.PilotRoot 'bin') -Filter '*.exe' | Get-FileHash -Algorithm SHA256 | Select-Object Path,Hash)
    }
    $snapshot=Join-Path $run 'units'
    New-Item -ItemType Directory -Path $snapshot | Out-Null
    foreach ($name in @($c.TargetUnit,$c.MaintenanceUnit)+$c.Units) { Copy-Item -LiteralPath (Join-Path $unitDir $name) -Destination $snapshot }
    $git=(Get-Command git.exe).Source
    if ((Invoke-Native $git @('-C',$c.ProjectRoot,'rev-parse','HEAD') (Join-Path $run 'source-before.txt')) -ne 0) { throw 'Cannot record source revision' }
    if ((Invoke-Native $git @('-C',$c.ProjectRoot,'status','--short') (Join-Path $run 'changes-before.txt')) -ne 0) { throw 'Cannot record source changes' }
}
function Assert-SameUnitManager($c,$run) {
    $baseline=Get-Content -LiteralPath (Join-Path $run 'baseline.json') -Raw | ConvertFrom-Json
    $manager=Get-UnitManager $c
    if ($manager.ProcessId -ne $baseline.managerPid -or [string]$manager.CreationDate -ne $baseline.managerCreated) { throw 'Manager identity changed during maintenance' }
}
function Suspend-UnitWorkloads($c,$run) {
    # This persistent hold survives a manager crash/reboot. Individual service
    # enable links belong only to TargetUnit, not default.target or other roots.
    Invoke-UnitControl $c @('disable',$c.TargetUnit) $run 'hold'
    Invoke-UnitControl $c @('stop',$c.TargetUnit) $run 'stop'
    Assert-NoRuntimes $c
    Assert-SameUnitManager $c $run
}
function Resume-UnitWorkloads($c,$run,[bool]$NewManager=$false) {
    Assert-NoAutostart $c
    if (-not $NewManager) { Assert-SameUnitManager $c $run }
    $manager=Get-UnitManager $c
    $baseline=Get-Content -LiteralPath (Join-Path $run 'baseline.json') -Raw | ConvertFrom-Json
    # Keep the boot hold until all services and listeners recover successfully.
    Invoke-UnitControl $c @('start',$c.TargetUnit) $run 'start'
    $deadline=(Get-Date).AddSeconds(180)
    do {
        $ready=$true
        foreach ($port in $c.Ports) { if (-not (Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue)) { $ready=$false } }
        if ($ready) { break }
        Start-Sleep -Seconds 2
    } while ((Get-Date) -lt $deadline)
    if (-not $ready) { throw 'Workload listeners did not recover' }
    $listing=Read-UnitList $c
    foreach ($name in $c.Units) { if ($listing -notmatch ([regex]::Escape($name)+'\s+loaded\s+active\s')) { throw "Workload failed recovery: $name" } }
    $all=@(Get-CimInstance Win32_Process)
    foreach ($port in $c.Ports) {
        foreach ($listener in @(Get-NetTCPConnection -State Listen -LocalPort $port)) {
            $cursor=[int]$listener.OwningProcess;$seen=@{}
            while ($cursor -ne $manager.ProcessId -and $cursor -gt 0 -and -not $seen.ContainsKey($cursor)) {
                $seen[$cursor]=$true
                $p=$all | Where-Object ProcessId -eq $cursor | Select-Object -First 1
                if (-not $p) { break };$cursor=[int]$p.ParentProcessId
            }
            if ($cursor -ne $manager.ProcessId) { throw 'A listener escaped manager ownership' }
        }
    }
    if ($baseline.targetEnabled) { Invoke-UnitControl $c @('enable',$c.TargetUnit) $run 'release-hold' }
    Write-Json (Join-Path $run 'health.json') @{managerPid=$manager.ProcessId;units=$listing;ports=$c.Ports;targetEnabled=$baseline.targetEnabled}
}
function Invoke-UnitTransaction($c,$run,$mode) {
    $state=[ordered]@{schema=1;action=$mode;phase='preflight';mutationStarted=$false;recoveryRequired=$false;success=$false;started=(Get-Date).ToUniversalTime().ToString('o')}
    $statePath=Join-Path $run 'result.json'
    $held=$false
    try {
        Write-Json $statePath $state
        Assert-UnitWorker $c
        Save-UnitSnapshot $c $run
        $held=$true
        $state.phase='stopping';$state.recoveryRequired=$true;Write-Json $statePath $state
        Suspend-UnitWorkloads $c $run
        if ($mode -eq 'FailAfterStop') { throw 'Injected failure after quiescence' }
        if ($mode -eq 'Update') {
            $state.phase='backup';Write-Json $statePath $state
            Backup-Home $c $run
            $state.phase='updating';$state.mutationStarted=$true;Write-Json $statePath $state
            Invoke-Update $c $run $true
        } else {
            $state.phase='planning';Write-Json $statePath $state
            Invoke-Update $c $run $false
        }
        $state.phase='restoring';Write-Json $statePath $state
        Resume-UnitWorkloads $c $run
        $held=$false;$state.phase='completed';$state.success=$true
    } catch {
        $state.error=$_.Exception.Message;$state.phase='failed'
        if ($held -and -not $state.mutationStarted) {
            $state.phase='recovering';Write-Json $statePath $state
            try { Resume-UnitWorkloads $c $run;$held=$false;$state.phase='failed-recovered' }
            catch { $state.phase='failed-recovery';$state.recoveryError=$_.Exception.Message }
        }
        if ($held) {
            # A partial recovery must not leave enabled or running workloads
            # against a possibly incomplete data/code migration.
            try { Invoke-UnitControl $c @('disable',$c.TargetUnit) $run 'retain-hold';Invoke-UnitControl $c @('stop',$c.TargetUnit) $run 'hold-stop' }
            catch { $state.holdError=$_.Exception.Message }
        }
    } finally {
        $state.recoveryRequired=$held;$state.completed=(Get-Date).ToUniversalTime().ToString('o')
        Write-Json $statePath $state
    }
    return $state
}
if ($Library) { return }
$c=Get-Content -LiteralPath $Config -Raw | ConvertFrom-Json
Assert-UnitMaintenanceConfig $c
New-Item -ItemType Directory -Path $c.StateDir -Force | Out-Null
$requestPath=Join-Path $c.StateDir 'request.json'
if ($Worker) {
    $lock=[IO.File]::Open((Join-Path $c.StateDir 'worker.lock'),'OpenOrCreate','ReadWrite','None')
    try {
        $request=Get-Content -LiteralPath $requestPath -Raw | ConvertFrom-Json
        if ($request.action -notin @('Rehearse','FailAfterStop','Update','Recover') -or $request.id -notmatch '^[a-f0-9]{32}$' -or $request.delaySeconds -lt 0 -or $request.delaySeconds -gt 120) { throw 'Invalid request' }
        $run=Join-Path $c.StateDir $request.id
        if ($request.action -eq 'Recover') {
            Assert-UnitWorker $c
            $statePath=Join-Path $run 'result.json'
            $prior=Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
            if ($prior.mutationStarted) { throw 'Update mutation started; inspect the backup and installed state before manual recovery' }
            if (Test-Path -LiteralPath (Join-Path $run 'baseline.json')) { Resume-UnitWorkloads $c $run $true }
            $prior.phase='manually-recovered';$prior.recoveryRequired=$false
            Write-Json $statePath $prior
            Remove-Item -LiteralPath $requestPath
            return
        }
        if (Test-Path -LiteralPath $run) { throw 'Request already consumed; inspect result before recovery' }
        New-Item -ItemType Directory -Path $run | Out-Null
        Copy-Item -LiteralPath $Config -Destination (Join-Path $run 'config.json')
        Copy-Item -LiteralPath $PSCommandPath -Destination (Join-Path $run 'worker.ps1')
        Write-Json (Join-Path $run 'result.json') @{schema=1;action=$request.action;phase='waiting';mutationStarted=$false;recoveryRequired=$false;success=$false}
        Start-Sleep -Seconds $request.delaySeconds
        $result=Invoke-UnitTransaction $c $run $request.action
        if (-not $result.recoveryRequired) { Remove-Item -LiteralPath $requestPath }
        if (-not $result.success) { exit 1 }
    } finally { $lock.Dispose() }
    return
}
if ($Action -eq 'Status') { Get-LatestResult $c.StateDir | ForEach-Object { Get-Content -LiteralPath $_.FullName };return }
if ($Action -eq 'Plan') {
    [pscustomobject]@{MaintenanceUnit=$c.MaintenanceUnit;TargetUnit=$c.TargetUnit;Pending=(Test-Path -LiteralPath $requestPath);StateDir=$c.StateDir}
    Read-UnitList $c
    return
}
$listing=Read-UnitList $c
if ($listing -match ([regex]::Escape($c.MaintenanceUnit)+'\s+loaded\s+(activating|active|deactivating)\s')) { throw 'Maintenance already running' }
if ($Action -eq 'Recover') {
    $request=Get-Content -LiteralPath $requestPath -Raw | ConvertFrom-Json
    $request.action='Recover';Write-Json $requestPath $request
} else {
    Assert-NoAutostart $c
    $request=@{schema=1;id=[guid]::NewGuid().ToString('N');action=$Action;delaySeconds=$DelaySeconds;queued=(Get-Date).ToUniversalTime().ToString('o')}
    $f=[IO.File]::Open($requestPath,'CreateNew','Write','None')
    try { $bytes=[Text.Encoding]::UTF8.GetBytes(($request | ConvertTo-Json));$f.Write($bytes,0,$bytes.Length);$f.Flush($true) } finally { $f.Dispose() }
}
$dispatch=Join-Path $c.StateDir ($request.id+'.dispatch.log')
$child=Start-Process -FilePath (Join-Path $c.PilotRoot 'bin\winctl.exe') -ArgumentList @('--user','start',$c.MaintenanceUnit) -WindowStyle Hidden -PassThru -RedirectStandardOutput $dispatch -RedirectStandardError ($dispatch+'.err')
$deadline=(Get-Date).AddSeconds(20)
do {
    $resultPath=Join-Path (Join-Path $c.StateDir $request.id) 'result.json'
    if (($Action -ne 'Recover' -and (Test-Path -LiteralPath $resultPath)) -or ($Action -eq 'Recover' -and -not (Test-Path -LiteralPath $requestPath))) {
        Write-Output "Queued $Action request $($request.id); $($c.MaintenanceUnit) owns execution. Use Status for the outcome."
        return
    }
    if ($child.HasExited) { throw "Dispatch exited; inspect $dispatch and the pending request" }
    Start-Sleep -Milliseconds 200
} while ((Get-Date) -lt $deadline)
throw "Could not confirm worker acceptance; retain request $($request.id) and inspect unit status"
