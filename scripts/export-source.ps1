#Requires -Version 7.2
[CmdletBinding()]
param([Parameter(Mandatory)][string]$Destination)
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Split-Path $PSScriptRoot -Parent))
$destinationPath = [IO.Path]::GetFullPath($Destination)
if (Test-Path -LiteralPath $destinationPath) { throw 'Destination already exists; refusing to overwrite an archive.' }
# Include current tracked and untracked source, including uncommitted work.
# .gitignore alone does NOT protect manually created archives. Explicitly deny
# runtime secrets even if someone accidentally stages/tracks them later.
$files = & git -C $root -c core.quotepath=false ls-files --cached --others --exclude-standard
if ($LASTEXITCODE -ne 0) { throw 'Cannot enumerate source files' }
Add-Type -AssemblyName System.IO.Compression.FileSystem
$archive = [IO.Compression.ZipFile]::Open($destinationPath,[IO.Compression.ZipArchiveMode]::Create)
try {
    foreach ($relative in ($files | Sort-Object -Unique)) {
        if ($relative -match '(^|/)(keys|\.git|\.vscode|bin)/|\.(pem|key|p12|pfx|zip|7z|log|exe|test)$') { continue }
        if ($relative -match '(^|/)\.env($|\.)' -and $relative -ne '.env.example') { continue }
        $path = [IO.Path]::GetFullPath((Join-Path $root $relative))
        if (!$path.StartsWith($root + [IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)) { throw 'Source path outside repository' }
        if ($path -eq $destinationPath -or !(Test-Path -LiteralPath $path -PathType Leaf)) { continue }
        if ((Get-Item -LiteralPath $path).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Refusing linked source file' }
        [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($archive,$path,$relative) | Out-Null
    }
} finally { $archive.Dispose() }
Write-Host 'Source archive created. Runtime secrets, keys and nested archives excluded; inspect before sharing.'
