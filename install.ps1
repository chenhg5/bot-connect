# Install bot-connect on Windows from GitHub Releases.
#
#   irm https://raw.githubusercontent.com/chenhg5/bot-connect/main/install.ps1 | iex
#
# Environment:
#   BOT_CONNECT_VERSION      version to install (default: newest release, pre-releases included)
#   BOT_CONNECT_INSTALL_DIR  where to put bot-connect.exe (default: %LOCALAPPDATA%\bot-connect\bin)
$ErrorActionPreference = "Stop"
$repo = "chenhg5/bot-connect"
$dir = if ($env:BOT_CONNECT_INSTALL_DIR) { $env:BOT_CONNECT_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "bot-connect\bin" }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  "AMD64" { "amd64" }
  "ARM64" { "arm64" }
  default { throw "unsupported architecture $($env:PROCESSOR_ARCHITECTURE)" }
}

$version = $env:BOT_CONNECT_VERSION
if (-not $version) {
  # /releases/latest skips pre-releases, so take the newest entry of the list.
  $version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases?per_page=1")[0].tag_name
}

$name = "bot-connect-$version-windows-$arch"
$base = "https://github.com/$repo/releases/download/$version"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("bot-connect-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Downloading bot-connect $version (windows/$arch)..."
  Invoke-WebRequest "$base/$name.zip" -OutFile "$tmp\$name.zip" -UseBasicParsing
  Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt" -UseBasicParsing

  $line = Get-Content "$tmp\checksums.txt" | Where-Object { $_ -match " $([regex]::Escape("$name.zip"))$" }
  if (-not $line) { throw "no checksum for $name.zip" }
  $expected = ($line -split '\s+')[0]
  $actual = (Get-FileHash "$tmp\$name.zip" -Algorithm SHA256).Hash.ToLower()
  if ($expected -ne $actual) { throw "checksum mismatch for $name.zip" }

  Expand-Archive "$tmp\$name.zip" -DestinationPath $tmp -Force
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  Copy-Item "$tmp\$name\bot-connect.exe" "$dir\bot-connect.exe" -Force
} finally {
  Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host "Installed $(& "$dir\bot-connect.exe" version) to $dir\bot-connect.exe"
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (($userPath -split ';') -notcontains $dir) {
  [Environment]::SetEnvironmentVariable("Path", "$userPath;$dir", "User")
  Write-Host "Added $dir to your user PATH (open a new terminal to use it)."
}
Write-Host "Next: create a config (see https://github.com/$repo/blob/$version/INSTALL.md), then run: bot-connect -config config.toml"
