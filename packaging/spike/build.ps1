param([ValidateSet('native', 'util', 'helper')][string]$Mode = 'helper')
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$previousToolchain = $env:GOTOOLCHAIN
$previousCGO = $env:CGO_ENABLED
Push-Location $repo
try {
	$env:GOTOOLCHAIN = 'go' + (Get-Content .go-version -Raw).Trim()
	$env:CGO_ENABLED = '0'
	$manifestPath = Join-Path $repo 'dist/msi-spike/build-manifest.json'
	if (Test-Path $manifestPath) { Remove-Item -LiteralPath $manifestPath }
	$artifacts = @()
	foreach ($version in @('0.0.1', '0.0.2')) {
		go build -trimpath -ldflags "-X main.version=$version" -o "dist/msi-spike/$version/fixture.exe" ./tools/msi-fixture
		if ($LASTEXITCODE -ne 0) { throw 'Fixture compilation failed' }
		Push-Location $PSScriptRoot
		try {
			# The maintainer approved the binary-release terms for this use.
			dotnet build Fixture.wixproj -p:AcceptEula=wix7 "-p:FixtureVersion=$version" "-p:FixtureMode=$Mode" --nologo
			if ($LASTEXITCODE -ne 0) { throw 'MSI compilation failed' }
		} finally { Pop-Location }
		foreach ($name in @('fixture.exe', "fixture-$version.msi")) {
			$file = Get-Item "dist/msi-spike/$version/$name"
			$artifacts += [ordered]@{version=$version; name=$name; sha256=(Get-FileHash $file.FullName).Hash.ToLowerInvariant(); size=$file.Length}
		}
	}
	[ordered]@{
		schema=1; mode=$Mode; commit=(git rev-parse HEAD); dirty=[bool](git status --porcelain)
		go=(go env GOVERSION); wix='7.0.0'; dotnet='10.0.400'; artifacts=$artifacts
	} | ConvertTo-Json -Depth 5 | Set-Content -Encoding UTF8 $manifestPath
} finally {
	Pop-Location
	$env:GOTOOLCHAIN = $previousToolchain
	$env:CGO_ENABLED = $previousCGO
}
