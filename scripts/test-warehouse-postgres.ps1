#Requires -Version 7.4
[CmdletBinding()]
param([switch]$IncludeHTTP)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$auth = Join-Path (Split-Path $root -Parent) 'Authentication'
$run = 'inventory-warehouse-test-' + [guid]::NewGuid().ToString('N')
$temporary = Join-Path ([IO.Path]::GetTempPath()) $run
$processes = [Collections.Generic.List[Diagnostics.Process]]::new()
$names = @('POSTGRES_PASSWORD','PGPASSWORD','INVENTORY_TEST_DATABASE_URL','WAREHOUSE_AUTH_DATABASE_URL','WAREHOUSE_INVENTORY_DATABASE_URL','WAREHOUSE_HTTP_ISOLATED','WAREHOUSE_AUTH_URL','WAREHOUSE_INVENTORY_URL','WAREHOUSE_PUBLIC_KEY')
$previous = @{}
foreach ($name in $names) { $previous[$name] = [Environment]::GetEnvironmentVariable($name,'Process') }
$created = $false
$serviceOutput = @{}
function Invoke-FixtureDocker([string[]]$Arguments) {
    $output = & docker @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw "Isolated Docker operation failed ($($Arguments[0])); output suppressed to protect credentials." }
    return $output
}
function SQL([string]$Database,[string]$Query) {
    $value = Invoke-FixtureDocker @('exec',$run,'psql','-X','-q','-t','-A','-v','ON_ERROR_STOP=1','-U','inventory_service','-d',$Database,'-c',$Query)
    return ([string]::Join("`n",[string[]]@($value))).Trim()
}
function Migrate([string]$Database,[string]$Directory,[string[]]$Operation) {
    # lib/pq reads PGPASSWORD from the environment; the URL/CLI contains no password.
    Invoke-FixtureDocker (@('run','--rm','--network',"container:$run",'-e','PGPASSWORD','-v',"${Directory}:/migrations:ro",'migrate/migrate:v4.18.3','-path=/migrations',"-database=postgres://inventory_service@127.0.0.1:5432/${Database}?sslmode=disable") + $Operation) | Out-Null
}
function AssertSQL([string]$Database,[string]$Query,[string]$Expected,[string]$Label) {
    if ((SQL $Database $Query) -ne $Expected) { throw "Assertion failed: $Label" }
    Write-Host "PASS: $Label = $Expected"
}
function FreePort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0)
    $listener.Start()
    try { return $listener.LocalEndpoint.Port } finally { $listener.Stop() }
}
function StartService([string]$Binary,[hashtable]$Settings) {
    $info = [Diagnostics.ProcessStartInfo]::new($Binary)
    $info.WorkingDirectory = $temporary # No regular .env can be loaded here.
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.Environment.Clear()
    foreach ($key in @('SystemRoot','WINDIR','PATH','TEMP','TMP')) {
        $value = [Environment]::GetEnvironmentVariable($key,'Process')
        if ($null -ne $value) { $info.Environment[$key] = $value }
    }
    foreach ($key in $Settings.Keys) { $info.Environment[$key] = $Settings[$key] }
    $process = [Diagnostics.Process]::Start($info)
    $processes.Add($process)
    # Drain without displaying service output, including failures.
    $stdoutTask = $process.StandardOutput.ReadToEndAsync()
    $serviceOutput[$process.Id] = @($stdoutTask, $process.StandardError.ReadToEndAsync())
    return $process
}
function WaitHTTP([string]$Base,[Diagnostics.Process]$Process) {
    for ($i=0;$i -lt 60;$i++) {
        if ($Process.HasExited) {
            $captured = ($serviceOutput[$Process.Id] | ForEach-Object { $_.GetAwaiter().GetResult() }) -join "`n"
            $category = 'unclassified startup failure'
            foreach ($pattern in @('password authentication failed','connection refused','bind:','private key','DATABASE_URL','JWT_','panic','invalid port','cannot parse','no such file','Access is denied')) {
                if ($captured.Contains($pattern)) { $category = $pattern; break }
            }
            throw "Disposable service $([IO.Path]::GetFileName($Process.StartInfo.FileName)) exited ($($Process.ExitCode)); safe category: $category. Raw output suppressed."
        }
        try { $r = Invoke-WebRequest "$Base/ready" -TimeoutSec 2; if ($r.StatusCode -eq 200) { return } } catch { }
        Start-Sleep -Milliseconds 500
    }
    throw 'Disposable HTTP service did not become ready'
}
try {
    New-Item -ItemType Directory -Path $temporary | Out-Null
    $bytes = [byte[]]::new(32)
    [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    $env:POSTGRES_PASSWORD = [Convert]::ToHexString($bytes).ToLowerInvariant()
    $env:PGPASSWORD = $env:POSTGRES_PASSWORD
    Invoke-FixtureDocker @('run','--rm','-d','--name',$run,'--tmpfs','/var/lib/postgresql/data','-e','POSTGRES_USER=inventory_service','-e','POSTGRES_PASSWORD','-e','POSTGRES_DB=inventory_warehouse_test','-p','127.0.0.1::5432','postgres:17-alpine') | Out-Null
    $created = $true
    $port = ([string](Invoke-FixtureDocker @('port',$run,'5432/tcp')) -split ':')[-1]
    $ready = $false
    for ($i=0;$i -lt 60;$i++) {
        try { if ((SQL 'inventory_warehouse_test' 'SELECT 1') -eq '1') { $ready=$true;break } } catch { }
        Start-Sleep -Seconds 1
    }
    if (!$ready) { throw 'Intended disposable database not ready' }
    SQL 'inventory_warehouse_test' 'CREATE DATABASE auth_warehouse_test' | Out-Null
    $inventoryMigrations = Join-Path $root 'db/migrations'
    $authMigrations = Join-Path $auth 'db/migrations'
    Migrate 'inventory_warehouse_test' $inventoryMigrations @('up')
    AssertSQL 'inventory_warehouse_test' "SELECT version::text || ':' || dirty::text FROM schema_migrations" '1:false' 'Inventory migration up'
    Migrate 'inventory_warehouse_test' $inventoryMigrations @('down','-all')
    AssertSQL 'inventory_warehouse_test' 'SELECT count(*) FROM schema_migrations' '0' 'Inventory complete rollback version rows'
    AssertSQL 'inventory_warehouse_test' "SELECT to_regclass('warehouses') IS NULL AND to_regprocedure('enforce_one_active_default_warehouse()') IS NULL AND NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='warehouses_exactly_one_default')" 't' 'Inventory table/function/trigger cleanup'
    Migrate 'inventory_warehouse_test' $inventoryMigrations @('up')
    AssertSQL 'inventory_warehouse_test' "SELECT version::text || ':' || dirty::text FROM schema_migrations" '1:false' 'Inventory migration reapply'
    Migrate 'auth_warehouse_test' $authMigrations @('up')
    AssertSQL 'auth_warehouse_test' "SELECT version::text || ':' || dirty::text FROM schema_migrations" '6:false' 'Auth prerequisite and warehouse migrations'
    $definitions = "SELECT string_agg(action,',' ORDER BY action) FROM identity_permissions WHERE resource='inventory.warehouses'"
    AssertSQL 'auth_warehouse_test' $definitions 'create,read,update' 'Auth warehouse definitions'
    AssertSQL 'auth_warehouse_test' 'SELECT (SELECT count(*) FROM identity_users)+(SELECT count(*) FROM identity_user_roles)+(SELECT count(*) FROM identity_role_permissions)' '0' 'No automatic identities or grants'
    Migrate 'auth_warehouse_test' $authMigrations @('down','1')
    AssertSQL 'auth_warehouse_test' "SELECT version::text || ':' || dirty::text FROM schema_migrations" '5:false' 'Auth warehouse rollback version'
    AssertSQL 'auth_warehouse_test' $definitions 'create,read,update' 'Auth rollback intentionally retains definitions'
    Migrate 'auth_warehouse_test' $authMigrations @('up')
    AssertSQL 'auth_warehouse_test' "SELECT version::text || ':' || dirty::text FROM schema_migrations" '6:false' 'Auth reapply'
    Migrate 'auth_warehouse_test' $authMigrations @('down','-all')
    AssertSQL 'auth_warehouse_test' 'SELECT count(*) FROM schema_migrations' '0' 'Auth complete rollback version rows'
    AssertSQL 'auth_warehouse_test' "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name<>'schema_migrations'" '0' 'Auth complete rollback tables'
    Migrate 'auth_warehouse_test' $authMigrations @('up')
    AssertSQL 'auth_warehouse_test' "SELECT version::text || ':' || dirty::text FROM schema_migrations" '6:false' 'Auth complete reapply'
    $env:INVENTORY_TEST_DATABASE_URL = "postgres://inventory_service:$($env:POSTGRES_PASSWORD)@127.0.0.1:$port/inventory_warehouse_test?sslmode=disable"
    & go -C $root test -count=1 -v ./internal/repository/postgres -run Integration
    if ($LASTEXITCODE -ne 0) { throw 'Warehouse repository integration failed' }
    if ($IncludeHTTP) {
        # Honor the application's fixed identity, only inside our new container.
        SQL 'inventory_warehouse_test' 'CREATE DATABASE inventory_db' | Out-Null
        Migrate 'inventory_db' $inventoryMigrations @('up')
        $env:WAREHOUSE_INVENTORY_DATABASE_URL = $env:INVENTORY_TEST_DATABASE_URL.Replace('/inventory_warehouse_test?', '/inventory_db?')
        $rsa = [Security.Cryptography.RSA]::Create(2048)
        try {
            [IO.File]::WriteAllText((Join-Path $temporary 'private.pem'),$rsa.ExportRSAPrivateKeyPem())
            [IO.File]::WriteAllText((Join-Path $temporary 'public.pem'),$rsa.ExportSubjectPublicKeyInfoPem())
        } finally { $rsa.Dispose() }
        $authBinary = Join-Path $temporary 'auth.exe'
        $inventoryBinary = Join-Path $temporary 'inventory.exe'
        & go -C $auth build -o $authBinary ./cmd/server
        if ($LASTEXITCODE -ne 0) { throw 'Auth fixture build failed' }
        & go -C $root build -o $inventoryBinary ./cmd/server
        if ($LASTEXITCODE -ne 0) { throw 'Inventory fixture build failed' }
        $authPort = FreePort
        $inventoryPort = FreePort
        $env:WAREHOUSE_AUTH_DATABASE_URL = "postgres://inventory_service:$($env:POSTGRES_PASSWORD)@127.0.0.1:$port/auth_warehouse_test?sslmode=disable"
        $env:WAREHOUSE_AUTH_URL = "http://127.0.0.1:$authPort"
        $env:WAREHOUSE_INVENTORY_URL = "http://127.0.0.1:$inventoryPort"
        $env:WAREHOUSE_PUBLIC_KEY = Join-Path $temporary 'public.pem'
        $env:WAREHOUSE_HTTP_ISOLATED = '1'
        $authProcess = StartService $authBinary @{DATABASE_URL=$env:WAREHOUSE_AUTH_DATABASE_URL;JWT_PRIVATE_KEY_PATH=(Join-Path $temporary 'private.pem');JWT_ISSUER='erp-auth';JWT_AUDIENCE='erp-api';JWT_EXPIRY='15m';SMTP_HOST='mock';PORT=[string]$authPort}
        $inventoryProcess = StartService $inventoryBinary @{DATABASE_URL=$env:WAREHOUSE_INVENTORY_DATABASE_URL;JWT_PUBLIC_KEY_PATH=$env:WAREHOUSE_PUBLIC_KEY;JWT_ISSUER='erp-auth';JWT_AUDIENCE='erp-api';PORT=[string]$inventoryPort}
        WaitHTTP $env:WAREHOUSE_AUTH_URL $authProcess
        WaitHTTP $env:WAREHOUSE_INVENTORY_URL $inventoryProcess
        & go -C $root test -count=1 -v ./internal/integration -run TestIsolatedAuthInventoryHTTP
        if ($LASTEXITCODE -ne 0) { throw 'Direct Auth/Inventory HTTP integration failed' }
    }
    Write-Host 'PASS: disposable migrations and repository checks; HTTP runs only with -IncludeHTTP.'
} finally {
    foreach ($process in $processes) {
        if (!$process.HasExited) { $process.Kill($true); $process.WaitForExit() }
        $process.Dispose()
    }
    if ($created) {
        Invoke-FixtureDocker @('rm','-f',$run) | Out-Null
        $remaining = Invoke-FixtureDocker @('ps','-a','--filter',"name=^/$run$",'--format','{{.Names}}')
        if ($remaining) { throw 'Disposable container cleanup failed' }
        Write-Host 'PASS: disposable service processes stopped and PostgreSQL container removed (tmpfs fixture data removed).'
    }
    foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name,$previous[$name],'Process') }
    $resolved = [IO.Path]::GetFullPath($temporary)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (!$resolved.StartsWith($tempRoot,[StringComparison]::OrdinalIgnoreCase) -or (Split-Path $resolved -Leaf) -ne $run) { throw 'Refusing cleanup outside generated temporary directory' }
    if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force }
    Write-Host 'PASS: temporary keys/binaries removed; prior environment restored.'
}
