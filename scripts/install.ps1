# Plan-Forge installer (Windows, PowerShell 5.1+).
#
# Downloads the windows-amd64 release zip, verifies its SHA-256 against
# checksums.txt, installs into %LOCALAPPDATA%\Programs\plan-forge, adds that
# folder to the user Path, then registers agent skills via
# `plan-forge install` (unless PLAN_FORGE_NO_SKILLS=1).
#
# Wrapped in a function so `irm | iex` never leaks preferences into the
# caller's session and never calls exit. Failures are raised with throw;
# $global:LASTEXITCODE carries the installer's exit code before throwing.

[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

function Install-PlanForge {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'

    $repo = 'danielckv/agentic-plan-forge'

    # amd64 only; other architectures exit with a message.
    if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64') {
        throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE (only AMD64 is supported)"
    }

    $version = $env:PLAN_FORGE_VERSION
    if (-not $version) { $version = 'latest' }

    $installDir = $env:PLAN_FORGE_INSTALL_DIR
    if (-not $installDir) {
        $installDir = Join-Path $env:LOCALAPPDATA 'Programs\plan-forge'
    }

    if ($version -eq 'latest') {
        $basePath = "https://github.com/$repo/releases/latest/download/"
    } else {
        $trimmed = $version.TrimStart('v')
        $basePath = "https://github.com/$repo/releases/download/v$trimmed/"
    }
    $archive = 'plan-forge-windows-amd64.zip'

    # --- download -----------------------------------------------------------
    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("plan-forge-" + [System.IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
    $archivePath = Join-Path $tmpDir $archive
    $checksumsPath = Join-Path $tmpDir 'checksums.txt'

    Invoke-WebRequest -UseBasicParsing -Uri ($basePath + $archive) -OutFile $archivePath
    Invoke-WebRequest -UseBasicParsing -Uri ($basePath + 'checksums.txt') -OutFile $checksumsPath

    # --- checksum verification (guards corrupt downloads only) ------------
    $want = (Select-String -Path $checksumsPath -Pattern ([regex]::Escape($archive)) |
        Select-Object -First 1).Line
    if (-not $want) { throw "checksums.txt has no entry for $archive" }
    $wantHash = ($want -split '\s+')[0]
    $gotHash = (Get-FileHash -Algorithm SHA256 -Path $archivePath).Hash.ToLower()
    if ($wantHash.ToLower() -ne $gotHash) {
        throw "checksum mismatch for $archive (want $wantHash, got $gotHash)"
    }

    # --- extraction --------------------------------------------------------
    $extractDir = Join-Path $tmpDir 'x'
    Expand-Archive -Path $archivePath -DestinationPath $extractDir -Force
    $newExe = Join-Path $extractDir 'plan-forge.exe'
    if (-not (Test-Path $newExe)) { throw 'archive does not contain plan-forge.exe' }

    # --- atomic-ish install --------------------------------------------------
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    $dest = Join-Path $installDir 'plan-forge.exe'
    if (Test-Path $dest) {
        # A running executable can be renamed but not overwritten.
        $old = "$dest.old"
        if (Test-Path $old) { Remove-Item $old -Force }
        Rename-Item -Path $dest -NewName 'plan-forge.exe.old' -Force
    }
    Move-Item -Path $newExe -Destination $dest -Force

    # --- PATH ---------------------------------------------------------------
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($userPath -notlike "*$installDir*") {
        [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    }

    Write-Output "Installed $dest"
    Write-Output 'Open a new terminal, then run: plan-forge version'

    # --- skill registration -------------------------------------------------
    if ($env:PLAN_FORGE_NO_SKILLS -eq '1') { return }

    & $dest install --host auto --scope user --yes
    $code = $LASTEXITCODE
    $global:LASTEXITCODE = $code
    if ($code -eq 0) { return }
    if ($code -eq 3) {
        throw "plan-forge is installed, but no supported agent (Claude Code, OMP, Antigravity) was found. Install one, then run: $dest install"
    }
    throw "skill registration failed (exit $code); see the installer output above"
}

Install-PlanForge
