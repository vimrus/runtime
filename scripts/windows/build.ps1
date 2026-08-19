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

$phpRoot = Join-Path $Work "php"
$develRoot = (Get-ChildItem (Join-Path $Work "devel") -Directory | Select-Object -First 1).FullName
if (-not $develRoot) { $develRoot = Join-Path $Work "devel" }

# FrankenPHP's Windows C sources require pthread.h; upstream provides it via
# the vcpkg pthreads port. GitHub-hosted Windows images ship vcpkg.
$vcpkgRoot = if ($env:VCPKG_INSTALLATION_ROOT) { $env:VCPKG_INSTALLATION_ROOT } else { "C:\vcpkg" }
$vcpkgExe = Join-Path $vcpkgRoot "vcpkg.exe"
if (-not (Test-Path $vcpkgExe)) { throw "vcpkg not found at $vcpkgExe" }
& $vcpkgExe install pthreads --triplet x64-windows
if ($LASTEXITCODE -ne 0) { throw "vcpkg install pthreads failed" }
$vcpkgInclude = Join-Path $vcpkgRoot "installed\x64-windows\include"
$vcpkgLib = Join-Path $vcpkgRoot "installed\x64-windows\lib"

$loader = Get-ChildItem -Path (Join-Path $Work "ioncube") -Recurse -Filter $Lock.ioncube.windows_x64.loader -File | Select-Object -First 1
if (-not $loader) { throw "ionCube Loader missing in $(Join-Path $Work 'ioncube')" }
$loaderPath = $loader.FullName

$env:CGO_ENABLED = "1"
$env:CC = "clang"
$env:CXX = "clang++"
$develInclude = Join-Path $develRoot "include"
$env:CGO_CFLAGS = "-DFRANKENPHP_VERSION=$($Lock.frankenphp.version) -I$vcpkgInclude -I$develInclude -I$develInclude\main -I$develInclude\TSRM -I$develInclude\Zend -I$develInclude\ext"
$develLib = Join-Path $develRoot "lib"
if (-not (Test-Path (Join-Path $develLib "php8ts.lib"))) {
    throw "php8ts.lib not found in devel pack: $develLib"
}
$env:CGO_LDFLAGS = "-L$vcpkgLib -L$phpRoot -L$develLib -lphp8ts -lphp8embed"

Push-Location $RepoRoot
try {
    go build `
        -tags "nobadger nomysql nopgx nowatcher nobrotli nomercure" `
        -ldflags "-s -w -X main.runtimeVersion=dev -X main.frankenPHPVersion=$($Lock.frankenphp.version) -X main.caddyVersion=$($Lock.caddy.version) -extldflags=-fuse-ld=lld" `
        -o (Join-Path $Staging "runtime\zentao-runtime.exe") `
        .\cmd\zentao-runtime
} finally {
    Pop-Location
}

Copy-Item (Join-Path $phpRoot "php.exe") (Join-Path $Staging "runtime\php.exe")
Copy-Item (Join-Path $phpRoot "php8ts.dll") (Join-Path $Staging "runtime\php8ts.dll")
Copy-Item $loaderPath (Join-Path $Staging "runtime\ioncube_loader_win_8.4.dll")
New-Item -ItemType Directory -Force -Path (Join-Path $Staging "runtime\ext") | Out-Null
foreach ($extension in @("php_opcache.dll", "php_pdo_mysql.dll", "php_mysqli.dll", "php_pdo_pgsql.dll", "php_curl.dll", "php_gd.dll", "php_mbstring.dll", "php_openssl.dll", "php_intl.dll", "php_ldap.dll", "php_bcmath.dll", "php_sockets.dll", "php_zip.dll")) {
    $source = Join-Path (Join-Path $phpRoot "ext") $extension
    if (Test-Path $source) { Copy-Item $source (Join-Path $Staging "runtime\ext") }
}
Copy-Item (Join-Path $phpRoot "*.dll") (Join-Path $Staging "runtime") -ErrorAction SilentlyContinue

New-Item -ItemType Directory -Force -Path (Join-Path $Staging "config\conf.d"), (Join-Path $Staging "licenses") | Out-Null
Copy-Item (Join-Path $RepoRoot "config\runtime.example.json") (Join-Path $Staging "config\runtime.example.json")
Copy-Item (Join-Path $RepoRoot "packaging\poc\php.ini") (Join-Path $Staging "config\php.ini")

& (Join-Path $Staging "runtime\php.exe") -v | Out-Null
$ioncubeDll = (Resolve-Path (Join-Path $Staging "runtime\ioncube_loader_win_8.4.dll")).Path
& (Join-Path $Staging "runtime\php.exe") -d "zend_extension=$ioncubeDll" -r "exit(extension_loaded('ionCube Loader') ? 0 : 1);"
if ($LASTEXITCODE -ne 0) { throw "ionCube Loader did not load on Windows PHP" }

Write-Host "Windows Runtime staged: $Staging"
