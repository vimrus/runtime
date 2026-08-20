<#
.SYNOPSIS
Builds the locked DuckDB static libraries for Windows x86_64 from source with
MSVC, producing COFF .lib archives compatible with the clang/lld + MSVC PHP
toolchain used by build.ps1.

.DESCRIPTION
DuckDB's official Windows prebuilt archives are MinGW GNU archives that depend
on libstdc++, which cannot be linked into the current Windows Runtime. This
script pins the DuckDB commit from versions.lock.json, downloads the source
tarball, and builds the static library targets with the MSVC toolchain found
through vswhere (Visual Studio 2022 or 2026) using Ninja. The resulting COFF
.lib files are collected under:

  <repo>/.cache/duckdb-windows/lib

and are linked by build.ps1 through the duckdb_use_static_lib Go build tag.

.PARAMETER Rebuild
Force a clean reconfiguration and rebuild, even when the artifacts already
exist in the cache.

.PARAMETER Cache
Optional cache root. Defaults to <repo>/.cache/duckdb-windows.
#>
param(
    [switch]$Rebuild,
    [string]$Cache = ""
)

$ErrorActionPreference = "Stop"
$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$Lock = Get-Content (Join-Path $RepoRoot "versions.lock.json") -Raw | ConvertFrom-Json

$commit = $Lock.duckdb.commit
if (-not $commit -or $commit -notmatch "^[0-9a-f]{40}$") {
    throw "versions.lock.json is missing a valid duckdb.commit"
}

$cacheRoot = if ($Cache) { $Cache } else { Join-Path $RepoRoot ".cache\duckdb-windows" }
$srcRoot = Join-Path $cacheRoot "src"
$buildRoot = Join-Path $cacheRoot "build"
$libDir = Join-Path $cacheRoot "lib"

if (-not $Rebuild -and (Test-Path (Join-Path $libDir "duckdb_static.lib"))) {
    Write-Host "DuckDB static libraries already built: $libDir"
    return
}

New-Item -ItemType Directory -Force -Path $cacheRoot, $srcRoot, $libDir | Out-Null

# 1. Fetch the pinned source tarball (codeload keeps the tarball stable per commit).
$tarball = Join-Path $cacheRoot "duckdb-$($commit.Substring(0, 12)).tar.gz"
if (-not (Test-Path $tarball)) {
    $url = "https://codeload.github.com/duckdb/duckdb/tar.gz/$commit"
    Write-Host "Downloading DuckDB source from $url"
    & curl.exe --fail --location --retry 3 --output $tarball $url
    if ($LASTEXITCODE -ne 0) { throw "failed to download DuckDB source" }
}
if ($Lock.duckdb.source_sha256) {
    $actual = (Get-FileHash -Algorithm SHA256 $tarball).Hash.ToLowerInvariant()
    $expected = $Lock.duckdb.source_sha256.ToLowerInvariant()
    if ($actual -ne $expected) {
        throw "DuckDB source SHA256 mismatch: expected $expected got $actual"
    }
}

# 2. Extract (skip when already extracted).
if (-not (Test-Path (Join-Path $srcRoot "CMakeLists.txt"))) {
    Write-Host "Extracting DuckDB source"
    Get-ChildItem $srcRoot -Force | Remove-Item -Recurse -Force
    & tar.exe -xzf $tarball -C $srcRoot --strip-components=1
    if ($LASTEXITCODE -ne 0) { throw "failed to extract DuckDB source" }
}

# 3. Locate the Visual Studio installation and set up the MSVC + Windows SDK
#    include/library paths for cl.exe, then configure with Ninja.
$vswhere = Join-Path ${env:ProgramFiles(x86)} "Microsoft Visual Studio\Installer\vswhere.exe"
if (-not (Test-Path $vswhere)) {
    $vswhere = "C:\Program Files (x86)\Microsoft Visual Studio\Installer\vswhere.exe"
}
if (-not (Test-Path $vswhere)) {
    throw "vswhere.exe not found; Visual Studio C++ workload is required"
}
$vsInstall = & $vswhere -latest -products * `
    -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 `
    -property installationPath
if (-not $vsInstall -or -not (Test-Path $vsInstall)) {
    throw "Visual Studio C++ toolset not found via vswhere"
}
$msvcRoot = Get-ChildItem (Join-Path $vsInstall "VC\Tools\MSVC") -Directory |
    Sort-Object Name -Descending | Select-Object -First 1
if (-not $msvcRoot) {
    throw "MSVC toolset not found under $vsInstall\VC\Tools\MSVC"
}
Write-Host "Using Visual Studio at $vsInstall"

$sdkRoot = (Get-ItemProperty "HKLM:\SOFTWARE\Microsoft\Windows Kits\InstalledRoots" -ErrorAction SilentlyContinue).KitsRoot10
$sdkVersion = ""
if ($sdkRoot -and (Test-Path (Join-Path $sdkRoot "Include"))) {
    $sdkVersion = (Get-ChildItem (Join-Path $sdkRoot "Include") -Directory |
        Sort-Object Name -Descending | Select-Object -First 1).Name
}
if (-not $sdkVersion) {
    throw "Windows SDK not found under $sdkRoot\Include"
}

$env:INCLUDE = @(
    (Join-Path $msvcRoot.FullName "include"),
    (Join-Path $msvcRoot.FullName "atlmfc\include"),
    (Join-Path $sdkRoot "Include\$sdkVersion\ucrt"),
    (Join-Path $sdkRoot "Include\$sdkVersion\um"),
    (Join-Path $sdkRoot "Include\$sdkVersion\shared"),
    (Join-Path $sdkRoot "Include\$sdkVersion\winrt")
) -join ";"
$env:LIB = @(
    (Join-Path $msvcRoot.FullName "lib\x64"),
    (Join-Path $sdkRoot "Lib\$sdkVersion\ucrt\x64"),
    (Join-Path $sdkRoot "Lib\$sdkVersion\um\x64")
) -join ";"
$env:PATH = (Join-Path $msvcRoot.FullName "bin\Hostx64\x64") + ";" +
    (Join-Path $sdkRoot "bin\$sdkVersion\x64") + ";" + $env:PATH

if ($Rebuild -and (Test-Path $buildRoot)) {
    Get-ChildItem $buildRoot -Force | Remove-Item -Recurse -Force
}
if (-not (Test-Path (Join-Path $buildRoot "CMakeCache.txt"))) {
    Write-Host "Configuring DuckDB build (Ninja + MSVC x64, Release)"
    $configureArgs = @(
        "-S", $srcRoot,
        "-B", $buildRoot,
        "-G", "Ninja",
        "-DCMAKE_BUILD_TYPE=Release",
        "-DCMAKE_MSVC_RUNTIME_LIBRARY=MultiThreadedDLL",
        "-DCMAKE_C_COMPILER=cl",
        "-DCMAKE_CXX_COMPILER=cl",
        "-DOVERRIDE_GIT_DESCRIBE=v$($Lock.duckdb.version)",
        # extension_config.cmake already links core_functions and parquet;
        # json is added for the JSON columns used by the observability writer.
        "-DBUILD_EXTENSIONS=json",
        "-DBUILD_SHELL=OFF",
        "-DBUILD_UNITTESTS=OFF",
        "-DBUILD_BENCHMARKS=OFF"
    )
    & cmake @configureArgs
    if ($LASTEXITCODE -ne 0) { throw "DuckDB CMake configure failed" }
}

# 4. Build the static core, the generated loader and every statically linked
#    extension. All are COFF .lib archives.
$targets = @(
    "duckdb_static",
    "duckdb_generated_extension_loader",
    "core_functions_extension",
    "json_extension",
    "parquet_extension"
)
Write-Host "Building DuckDB static libraries (this can take a while)"
& cmake --build $buildRoot --target $targets
if ($LASTEXITCODE -ne 0) { throw "DuckDB CMake build failed" }

# 5. Collect and verify every expected library into the flat lib directory.
$expectedLibs = @(
    "duckdb_static.lib",
    "duckdb_generated_extension_loader.lib",
    "core_functions_extension.lib",
    "json_extension.lib",
    "parquet_extension.lib"
)
foreach ($libName in $expectedLibs) {
    $libSource = Get-ChildItem -Path $buildRoot -Recurse -Filter $libName -File |
        Select-Object -First 1
    if (-not $libSource) {
        throw "expected DuckDB library missing: $libName"
    }
    Copy-Item $libSource.FullName (Join-Path $libDir $libName) -Force
}

$totalBytes = (Get-ChildItem $libDir -Filter "*.lib" | Measure-Object -Property Length -Sum).Sum
Write-Host "DuckDB static libraries built: $libDir ($([math]::Round($totalBytes / 1MB, 1)) MB)"
