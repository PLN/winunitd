param([string]$PackageVersion = '0.1.0', [switch]$AllowDirty)
$ErrorActionPreference = 'Stop'
# Installer 0.1.0 is paired with ProductCode 78C43374-5AB7-4E81-B9CF-09E8ACD01133.
# Record a new ProductCode in source before publishing another installer version.
if ($PackageVersion -ne '0.1.0') { throw 'Record a new ProductCode before changing the installer version' }
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
Push-Location $repo
try {
	if ((git status --porcelain) -and !$AllowDirty) { throw 'Clean source required; use AllowDirty only for development' }
	$packageManifest = "dist/wix/$PackageVersion/package-manifest.json"
	if (Test-Path -LiteralPath $packageManifest) { Remove-Item -LiteralPath $packageManifest }
	$release = '0.1.0-alpha'
	go run ./tools/build -out dist/wix/payload -version $release
	if ($LASTEXITCODE) { throw 'Payload build failed' }
	# The helper evaluates the compatibility floor with its own identity on
	# the payload's behalf, so it is built like the payload and the produced
	# binaries must agree before the package is built.
	$priorCgo = $env:CGO_ENABLED
	$env:CGO_ENABLED = '0'
	try {
		go build -trimpath -buildvcs=true -ldflags "-X github.com/PLN/winunitd/internal/version.Version=$release" -o dist/wix/payload/msi-check.exe ./tools/msi-check
		if ($LASTEXITCODE) { throw 'Service helper build failed' }
	} finally {
		if ($null -eq $priorCgo) { Remove-Item Env:CGO_ENABLED } else { $env:CGO_ENABLED = $priorCgo }
	}
	$identityArgs = @('-manifest', 'dist/wix/payload/build-manifest.json', '-release', $release, '-helper', 'dist/wix/payload/msi-check.exe')
	if ($AllowDirty) { $identityArgs += '-development' }
	$identity = go run ./tools/package-identity @identityArgs
	if ($LASTEXITCODE) { throw 'Helper and payload are not one build' }
	$identity = $identity | ConvertFrom-Json
	$compiler = (Get-Command gcc -ErrorAction Stop).Source
	$compilerVersion = & $compiler -dumpfullversion
	if ($LASTEXITCODE) { throw 'C compiler version probe failed' }
	if ($compilerVersion -ne '16.1.0') { throw 'MSI token helper requires GCC 16.1.0' }
	& $compiler -shared -nostdlib -O2 -Wall -Wextra -Werror '-Wl,--no-insert-timestamp,--entry,0' -o dist/wix/payload/msi-token.dll tools/msi-token/token.c -lmsi -lbcrypt -lkernel32
	if ($LASTEXITCODE) { throw 'MSI transaction identity helper build failed' }
	Push-Location $PSScriptRoot
	try {
		dotnet build Winunitd.wixproj -p:AcceptEula=wix7 "-p:PackageVersion=$PackageVersion" --nologo
		if ($LASTEXITCODE) { throw 'MSI build failed' }
	} finally { Pop-Location }
	$file = Get-Item "dist/wix/$PackageVersion/winunitd-$PackageVersion-x64.msi"
	$payload = Get-Content dist/wix/payload/build-manifest.json -Raw | ConvertFrom-Json
	[ordered]@{
		schema = 1
		commit = (git rev-parse HEAD)
		dirty = [bool](git status --porcelain)
		version = $release
		installerVersion = $PackageVersion
		# A development package (AllowDirty) is never admissible to servicing.
		admissible = $identity.admissible
		identity = $identity
		productCode = '78C43374-5AB7-4E81-B9CF-09E8ACD01133'
		upgradeCode = 'A512B91F-1883-40FD-8EDB-5B8C5708DEEA'
		signed = $false
		wix = '7.0.0'
		dotnet = '10.0.400'
		token_compiler = "gcc $compilerVersion"
		token_helper_sha256 = (Get-FileHash dist/wix/payload/msi-token.dll).Hash.ToLowerInvariant()
		helper_sha256 = (Get-FileHash dist/wix/payload/msi-check.exe).Hash.ToLowerInvariant()
		name = $file.Name
		size = $file.Length
		sha256 = (Get-FileHash $file.FullName).Hash.ToLowerInvariant()
		payload = $payload
	} | ConvertTo-Json -Depth 6 | Set-Content $packageManifest -Encoding UTF8
} finally { Pop-Location }
