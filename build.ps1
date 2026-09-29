# Cluster Route build script
#
# Artifacts:
#   build/bin/Cluster Route.exe                 desktop app (Wails)
#   cluster-route-server.exe                    headless service (console)
#   build/bin/Cluster Route-amd64-installer.exe  NSIS installer (-Installer)
#
# NOTE on VERSIONINFO:
#   The RT_VERSION resource generated internally by `wails build` (go-winres)
#   can be read by Explorer and version.dll, but NOT by .NET Framework's
#   FileVersionInfo (i.e. PowerShell's (Get-Item).VersionInfo) - fields such
#   as CompanyName come back empty. After linking, this script therefore
#   rewrites the version resource via tools/patchversion (Win32 UpdateResource
#   API, RC/goversioninfo-compatible layout, lang 0409). All three readers
#   (Explorer / version.dll / .NET) then work. Source of version fields:
#   build/windows/info.json.
#
# Usage:
#   .\build.ps1               # desktop + server
#   .\build.ps1 -Installer    # also build the NSIS installer
#   .\build.ps1 -SkipFrontend # skip frontend build (when web/dist is fresh)
#   .\build.ps1 -Sign         # sign artifacts with build\OpenALC-codesign.pfx
param(
    [switch]$Installer,
    [switch]$SkipFrontend,
    [switch]$Sign
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

# --- toolchain --------------------------------------------------------------
function Resolve-Tool([string]$Name, [string[]]$ExtraDirs) {
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    foreach ($d in $ExtraDirs) {
        $p = Join-Path $d "$Name.exe"
        if (Test-Path $p) { return $p }
    }
    throw "Tool not found: $Name (install it and add to PATH)"
}

$go       = Resolve-Tool "go"       @("C:\devlopment\Tools\go\bin", "$env:USERPROFILE\go\bin", "C:\Program Files\Go\bin")
$wails    = Resolve-Tool "wails"    @("$env:USERPROFILE\go\bin")
$makensis = Resolve-Tool "makensis" @("C:\devlopment\Tools\NSIS", "C:\Program Files (x86)\NSIS", "C:\Program Files\NSIS")
Write-Host "go       = $go"
Write-Host "wails    = $wails"
Write-Host "makensis = $makensis"

$patchArgs  = @("run", "./tools/patchversion")
$desktopExe = Join-Path $PSScriptRoot "build\bin\Cluster Route.exe"
$serverExe  = Join-Path $PSScriptRoot "cluster-route-server.exe"

# --- desktop app ------------------------------------------------------------
Write-Host ""
Write-Host "==> [1/4] wails build (desktop app)" -ForegroundColor Cyan
# -skipbindings: bindings generation (wailsbindings.exe) can hang under
# non-interactive shells; the frontend talks REST and does not use them.
$wailsArgs = @("build", "-skipbindings")
if ($SkipFrontend) { $wailsArgs += "-s" }
& $wails @($wailsArgs)
if ($LASTEXITCODE -ne 0) { throw "wails build failed" }

Write-Host "==> [2/4] patch desktop VERSIONINFO" -ForegroundColor Cyan
& $go @($patchArgs + @("-exe", $desktopExe))
if ($LASTEXITCODE -ne 0) { throw "failed to patch desktop version resource" }

# --- headless server --------------------------------------------------------
Write-Host "==> [3/4] go build (headless server)" -ForegroundColor Cyan
& $go @("build", "-o", $serverExe, ".")
if ($LASTEXITCODE -ne 0) { throw "go build failed" }
& $go @($patchArgs + @("-exe", $serverExe, "-original", "cluster-route-server.exe"))
if ($LASTEXITCODE -ne 0) { throw "failed to patch server version resource" }

# --- NSIS installer ---------------------------------------------------------
if ($Installer) {
    Write-Host "==> [4/4] makensis (installer)" -ForegroundColor Cyan
    # Package the already-patched exe with standalone makensis.
    # Do NOT use `wails build -nsis` here: it rebuilds the exe and
    # overwrites the version-resource patch above.
    Push-Location (Join-Path $PSScriptRoot "build\windows\installer")
    try {
        & $makensis @("-DARG_WAILS_AMD64_BINARY=..\..\bin\Cluster Route.exe", ".\project.nsi")
        if ($LASTEXITCODE -ne 0) { throw "makensis failed" }
    } finally {
        Pop-Location
    }
} else {
    Write-Host "==> [4/4] installer skipped (pass -Installer to build it)"
}

# --- optional signing -------------------------------------------------------
if ($Sign) {
    $signtool = Get-Command signtool -ErrorAction SilentlyContinue
    if (-not $signtool) {
        Write-Warning "signtool not found, skipping signing"
    } else {
        $targets = @($desktopExe)
        if ($Installer) { $targets += (Join-Path $PSScriptRoot "build\bin\Cluster Route-amd64-installer.exe") }
        & $signtool @("sign", "/f", "build\OpenALC-codesign.pfx", "/p", "OpenALC", "/fd", "SHA256") + $targets
    }
}

# --- verify artifacts ---------------------------------------------------------
Write-Host ""
Write-Host "==> verify artifact version info" -ForegroundColor Cyan
$checkTargets = @($desktopExe, $serverExe)
if ($Installer) { $checkTargets += (Join-Path $PSScriptRoot "build\bin\Cluster Route-amd64-installer.exe") }
foreach ($f in $checkTargets) {
    if (Test-Path $f) {
        $vi = [System.Diagnostics.FileVersionInfo]::GetVersionInfo($f)
        Write-Host ("  {0}" -f (Split-Path $f -Leaf))
        Write-Host ("    CompanyName    = [{0}]" -f $vi.CompanyName)
        Write-Host ("    ProductName    = [{0}]" -f $vi.ProductName)
        Write-Host ("    FileVersion    = [{0}]" -f $vi.FileVersion)
        if ($vi.CompanyName -ne "OpenALC") { Write-Warning "  CompanyName is NOT OpenALC!" }
    } else {
        Write-Warning "  missing artifact: $f"
    }
}
Write-Host ""
Write-Host "Build finished." -ForegroundColor Green
