param([string]$PackageVersion = '0.2.0', [switch]$AllowDirty)
$ErrorActionPreference = 'Stop'
if ($PackageVersion -notmatch '^\d+\.\d+\.\d+$') { throw 'Three-part MSI version required' }
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
Push-Location $repo
try {
	if ((git status --porcelain) -and !$AllowDirty) { throw 'Clean source required; use AllowDirty only for development' }
	$packageManifest = "dist/beta/$PackageVersion/package-manifest.json"
	if (Test-Path -LiteralPath $packageManifest) { Remove-Item -LiteralPath $packageManifest }
	go run ./tools/build -out dist/beta/payload -version "$PackageVersion-beta"
	if ($LASTEXITCODE) { throw 'Payload build failed' }
	go build -trimpath -o dist/beta/payload/msi-check.exe ./tools/msi-check
	if ($LASTEXITCODE) { throw 'Preflight build failed' }
	$compiler = (Get-Command gcc -ErrorAction Stop).Source
	$compilerVersion = & $compiler -dumpfullversion
	if ($LASTEXITCODE) { throw 'C compiler version probe failed' }
	if ($compilerVersion -ne '16.1.0') { throw 'MSI token helper requires GCC 16.1.0' }
	& $compiler -shared -O2 -Wall -Wextra -Werror '-Wl,--no-insert-timestamp' -o dist/beta/payload/msi-token.dll tools/msi-token/token.c -lmsi -lbcrypt
	if ($LASTEXITCODE) { throw 'MSI transaction identity helper build failed' }
	Push-Location $PSScriptRoot
	try {
		dotnet build Winunitd.wixproj -p:AcceptEula=wix7 "-p:PackageVersion=$PackageVersion" --nologo
		if ($LASTEXITCODE) { throw 'MSI build failed' }
	} finally { Pop-Location }
	$file = Get-Item "dist/beta/$PackageVersion/winunitd-$PackageVersion-x64-beta.msi"
	$payload = Get-Content dist/beta/payload/build-manifest.json -Raw | ConvertFrom-Json
	[ordered]@{schema=1; commit=(git rev-parse HEAD); dirty=[bool](git status --porcelain); version=$PackageVersion; signed=$false; wix='7.0.0'; dotnet='10.0.400'; token_compiler="gcc $compilerVersion"; token_helper_sha256=(Get-FileHash dist/beta/payload/msi-token.dll).Hash.ToLowerInvariant(); name=$file.Name; size=$file.Length; sha256=(Get-FileHash $file.FullName).Hash.ToLowerInvariant(); helper_sha256=(Get-FileHash dist/beta/payload/msi-check.exe).Hash.ToLowerInvariant(); payload=$payload} |
		ConvertTo-Json -Depth 6 | Set-Content $packageManifest -Encoding UTF8
} finally { Pop-Location }
