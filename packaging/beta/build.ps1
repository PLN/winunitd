param([string]$PackageVersion = '0.2.0', [switch]$AllowDirty)
$ErrorActionPreference = 'Stop'
if ($PackageVersion -notmatch '^\d+\.\d+\.\d+$') { throw 'Three-part MSI version required' }
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
Push-Location $repo
try {
	if ((git status --porcelain) -and !$AllowDirty) { throw 'Clean source required; use AllowDirty only for development' }
	$packageManifest = "dist/beta/$PackageVersion/package-manifest.json"
	if (Test-Path -LiteralPath $packageManifest) { Remove-Item -LiteralPath $packageManifest }
	$release = "$PackageVersion-beta"
	go run ./tools/build -out dist/beta/payload -version $release
	if ($LASTEXITCODE) { throw 'Payload build failed' }
	# Built like the payload; the produced binaries must agree (see packaging/wix/build.ps1).
	$priorCgo = $env:CGO_ENABLED
	$env:CGO_ENABLED = '0'
	try {
		go build -trimpath -buildvcs=true -ldflags "-X github.com/PLN/winunitd/internal/version.Version=$release" -o dist/beta/payload/msi-check.exe ./tools/msi-check
		if ($LASTEXITCODE) { throw 'Preflight build failed' }
	} finally {
		if ($null -eq $priorCgo) { Remove-Item Env:CGO_ENABLED } else { $env:CGO_ENABLED = $priorCgo }
	}
	$identityArgs = @('-manifest', 'dist/beta/payload/build-manifest.json', '-release', $release, '-helper', 'dist/beta/payload/msi-check.exe')
	if ($AllowDirty) { $identityArgs += '-development' }
	$identity = go run ./tools/package-identity @identityArgs
	if ($LASTEXITCODE) { throw 'Helper and payload are not one build' }
	$identity = $identity | ConvertFrom-Json
	$compiler = (Get-Command gcc -ErrorAction Stop).Source
	$compilerVersion = & $compiler -dumpfullversion
	if ($LASTEXITCODE) { throw 'C compiler version probe failed' }
	if ($compilerVersion -ne '16.1.0') { throw 'MSI token helper requires GCC 16.1.0' }
	& $compiler -shared -nostdlib -O2 -Wall -Wextra -Werror '-Wl,--no-insert-timestamp,--entry,0' -o dist/beta/payload/msi-token.dll tools/msi-token/token.c -lmsi -lbcrypt -lkernel32
	if ($LASTEXITCODE) { throw 'MSI transaction identity helper build failed' }
	Push-Location $PSScriptRoot
	try {
		dotnet build Winunitd.wixproj -p:AcceptEula=wix7 "-p:PackageVersion=$PackageVersion" --nologo
		if ($LASTEXITCODE) { throw 'MSI build failed' }
	} finally { Pop-Location }
	$file = Get-Item "dist/beta/$PackageVersion/winunitd-$PackageVersion-x64-beta.msi"
	$payload = Get-Content dist/beta/payload/build-manifest.json -Raw | ConvertFrom-Json
	[ordered]@{schema=1; commit=(git rev-parse HEAD); dirty=[bool](git status --porcelain); version=$PackageVersion; signed=$false; wix='7.0.0'; dotnet='10.0.400'; token_compiler="gcc $compilerVersion"; token_helper_sha256=(Get-FileHash dist/beta/payload/msi-token.dll).Hash.ToLowerInvariant(); name=$file.Name; size=$file.Length; sha256=(Get-FileHash $file.FullName).Hash.ToLowerInvariant(); helper_sha256=(Get-FileHash dist/beta/payload/msi-check.exe).Hash.ToLowerInvariant(); admissible=$identity.admissible; identity=$identity; payload=$payload} |
		ConvertTo-Json -Depth 6 | Set-Content $packageManifest -Encoding UTF8
} finally { Pop-Location }
