<#
.SYNOPSIS
Builds the ZenTao Runtime for Windows x86_64 using the locked PHP VS17 TS
artifacts and the self-maintained Go Host. Must run on a native Windows
runner with Visual Studio LLVM/Clang and Go installed.

.PARAMETER Staging
Output staging directory.
#>
param(
    [string]$Staging = "dist/runtime-windows-x64"
)

$ErrorActionPreference = "Stop"
$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$Lock = Get-Content (Join-Path $RepoRoot "versions.lock.json") -Raw | ConvertFrom-Json

if (-not $Lock.php.windows.runtime.url -or -not $Lock.php.windows.development.url) {
    throw "Windows PHP archives are not locked in versions.lock.json"
}
if (-not $Lock.ioncube.windows_x64.url) {
    throw "Windows ionCube Loader is not locked in versions.lock.json"
}

$Work = Join-Path $RepoRoot ".cache\windows-build"
New-Item -ItemType Directory -Force -Path $Work, $Staging | Out-Null

function Get-VerifiedFile {
    param([string]$Url, [string]$Sha256, [string]$Destination)
    if (-not (Test-Path $Destination)) {
        Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $Destination
    }
    $actual = (Get-FileHash -Algorithm SHA256 $Destination).Hash.ToLowerInvariant()
    if ($actual -ne $Sha256.ToLowerInvariant()) {
        throw "SHA256 mismatch for ${Url}: expected $Sha256 got $actual"
    }
}

$runtimeZip = Join-Path $Work "php-win.zip"
$develZip = Join-Path $Work "php-devel.zip"
$ioncubeZip = Join-Path $Work "ioncube-win.zip"
Get-VerifiedFile $Lock.php.windows.runtime.url $Lock.php.windows.runtime.sha256 $runtimeZip
Get-VerifiedFile $Lock.php.windows.development.url $Lock.php.windows.development.sha256 $develZip
Get-VerifiedFile $Lock.ioncube.windows_x64.url $Lock.ioncube.windows_x64.archive_sha256 $ioncubeZip

Expand-Archive -Path $runtimeZip -DestinationPath (Join-Path $Work "php") -Force
Expand-Archive -Path $develZip -DestinationPath (Join-Path $Work "devel") -Force
Expand-Archive -Path $ioncubeZip -DestinationPath (Join-Path $Work "ioncube") -Force

$phpRoot = (Get-ChildItem (Join-Path $Work "php") -Directory | Select-Object -First 1).FullName
if (-not $phpRoot) { $phpRoot = Join-Path $Work "php" }
$develRoot = (Get-ChildItem (Join-Path $Work "devel") -Directory | Select-Object -First 1).FullName
if (-not $develRoot) { $develRoot = Join-Path $Work "devel" }
$loader = Join-Path (Join-Path $Work "ioncube") $Lock.ioncube.windows_x64.loader
if (-not (Test-Path $loader)) { throw "ionCube Loader missing: $loader" }

$env:CGO_ENABLED = "1"
$env:CC = "clang"
$env:CXX = "clang++"
$env:CGO_CFLAGS = "-I$(Join-Path $develRoot 'include')"
$develLib = Join-Path $develRoot "lib"
if (-not (Test-Path (Join-Path $develLib "php8ts.lib"))) {
    throw "php8ts.lib not found in devel pack: $develLib"
}
$env:CGO_LDFLAGS = "-L$phpRoot -L$develLib -lphp8ts"

Push-Location $RepoRoot
try {
    go build `
        -tags "nobadger nomysql nopgx nowatcher nobrotli nomercure" `
        -ldflags "-s -w -X main.runtimeVersion=dev -X main.frankenPHPVersion=$($Lock.frankenphp.version) -X main.caddyVersion=$($Lock.caddy.version)" `
        -o (Join-Path $Staging "runtime\zentao-runtime.exe") `
        .\cmd\zentao-runtime
} finally {
    Pop-Location
}

Copy-Item (Join-Path $phpRoot "php.exe") (Join-Path $Staging "runtime\php.exe")
Copy-Item (Join-Path $phpRoot "php8ts.dll") (Join-Path $Staging "runtime\php8ts.dll")
Copy-Item $loader (Join-Path $Staging "runtime\ioncube_loader_win_8.4.dll")
New-Item -ItemType Directory -Force -Path (Join-Path $Staging "runtime\ext") | Out-Null
foreach ($extension in @("php_opcache.dll", "php_pdo_mysql.dll", "php_mysqli.dll", "php_pdo_pgsql.dll", "php_curl.dll", "php_gd.dll", "php_mbstring.dll", "php_openssl.dll", "php_intl.dll", "php_ldap.dll", "php_bcmath.dll", "php_sockets.dll", "php_zip.dll")) {
    $source = Join-Path (Join-Path $phpRoot "ext") $extension
    if (Test-Path $source) { Copy-Item $source (Join-Path $Staging "runtime\ext") }
}

New-Item -ItemType Directory -Force -Path (Join-Path $Staging "config\conf.d"), (Join-Path $Staging "licenses") | Out-Null
Copy-Item (Join-Path $RepoRoot "config\runtime.example.json") (Join-Path $Staging "config\runtime.example.json")
Copy-Item (Join-Path $RepoRoot "packaging\poc\php.ini") (Join-Path $Staging "config\php.ini")

& (Join-Path $Staging "runtime\php.exe") -v | Out-Null
& (Join-Path $Staging "runtime\php.exe") -d "zend_extension=$(Join-Path $Staging 'runtime\ioncube_loader_win_8.4.dll')" -r "exit(extension_loaded('ionCube Loader') ? 0 : 1);"
if ($LASTEXITCODE -ne 0) { throw "ionCube Loader did not load on Windows PHP" }

Write-Host "Windows Runtime staged: $Staging"
