# Install used on Windows and start it at login (no admin needed):
#
#   irm https://raw.githubusercontent.com/KilianSen/used/master/install.ps1 | iex
#
# Env: USED_VERSION (default: latest), USED_REPO (default: KilianSen/used),
#      USED_NO_SERVICE=1 to skip `used install`.
$ErrorActionPreference = 'Stop'

$repo    = if ($env:USED_REPO) { $env:USED_REPO } else { 'KilianSen/used' }
$version = if ($env:USED_VERSION) { $env:USED_VERSION } else { 'latest' }
$binDir  = Join-Path $env:LOCALAPPDATA 'Programs\used'

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}
$base  = if ($version -eq 'latest') { "https://github.com/$repo/releases/latest/download" }
         else { "https://github.com/$repo/releases/download/$version" }
$asset = "used_windows_$arch.zip"

$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null
try {
    Write-Host "downloading $asset ($version)"
    Invoke-WebRequest "$base/$asset" -OutFile "$tmp\$asset" -UseBasicParsing
    Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt" -UseBasicParsing

    $line = Select-String -Path "$tmp\checksums.txt" -Pattern " $([regex]::Escape($asset))$" | Select-Object -First 1
    if (-not $line) { throw "$asset not listed in checksums.txt" }
    $expected = ($line.Line -split '\s+')[0]
    $actual = (Get-FileHash "$tmp\$asset" -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected) { throw "checksum mismatch" }

    Expand-Archive "$tmp\$asset" -DestinationPath $tmp -Force
    New-Item -ItemType Directory $binDir -Force | Out-Null
    # Stop a running daemon so its binary can be replaced.
    if (Test-Path "$binDir\used.exe") { & "$binDir\used.exe" uninstall 2>$null | Out-Null }
    Copy-Item "$tmp\used.exe" "$binDir\used.exe" -Force
    Write-Host "installed $binDir\used.exe"
} finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $binDir) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$binDir", 'User')
    Write-Host "added $binDir to your PATH (open a new terminal)"
}

if ($env:USED_NO_SERVICE -ne '1') { & "$binDir\used.exe" install }
