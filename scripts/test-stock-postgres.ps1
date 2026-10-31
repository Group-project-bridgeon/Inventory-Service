#Requires -Version 7.4
[CmdletBinding()]
param([switch]$PostingOnly,[switch]$OpeningOnly,[switch]$AdjustmentsOnly)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$run = 'inventory-stock-test-' + [guid]::NewGuid().ToString('N')
$temporary = Join-Path $root ('.' + $run)
$containerID = $null
$containerAttempted = $false
$names = @('POSTGRES_PASSWORD','PGPASSWORD','STOCK_TEST_DATABASE_URL','STOCK_TEST_RUN_ID','STOCK_TEST_PORT','GOTMPDIR')
$previous = @{}
foreach ($name in $names) { $previous[$name] = [Environment]::GetEnvironmentVariable($name,'Process') }
function FixtureDocker([string[]]$Arguments) {
    $output = & docker @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw "Stock fixture Docker operation failed ($($Arguments[0])); raw output suppressed." }
    return $output
}
function CheckOwnership {
    if (!$containerID -or $containerID -notmatch '^[0-9a-f]{64}$') { throw 'No run-owned container identity' }
    $label = [string](FixtureDocker @('inspect','--format','{{index .Config.Labels "inventory.stock.fixture"}}',$containerID))
    if ($label.Trim() -ne $run) { throw 'Refusing operation on a container not owned by this run' }
}
function SQL([string]$Query) {
    CheckOwnership
    $result = FixtureDocker @('exec',$containerID,'psql','-X','-q','-t','-A','-v','ON_ERROR_STOP=1','-U','inventory_stock_fixture','-d','inventory_stock_test','-c',$Query)
    return ([string]::Join("`n",[string[]]@($result))).Trim()
}
function AssertSQL([string]$Query,[string]$Expected,[string]$Label) {
    if ((SQL $Query) -ne $Expected) { throw "Stock fixture assertion failed: $Label" }
    Write-Host "PASS: $Label"
}
function Migrate([string[]]$Operation) {
    CheckOwnership
    $migrations = Join-Path $root 'db/migrations'
    FixtureDocker (@('run','--rm','--network',"container:$containerID",'-e','PGPASSWORD','-v',"${migrations}:/migrations:ro",'migrate/migrate:v4.18.3','-path=/migrations','-database=postgres://inventory_stock_fixture@127.0.0.1:5432/inventory_stock_test?sslmode=disable') + $Operation) | Out-Null
}
try {
    $null = New-Item -ItemType Directory -Path $temporary
    $env:GOTMPDIR = $temporary
    $bytes = [byte[]]::new(32)
    [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    $env:POSTGRES_PASSWORD = [Convert]::ToHexString($bytes).ToLowerInvariant()
    $env:PGPASSWORD = $env:POSTGRES_PASSWORD
    $containerAttempted = $true
    $runOutput = @(FixtureDocker @('run','--rm','-d','--name',$run,'--label',"inventory.stock.fixture=$run",'--tmpfs','/var/lib/postgresql/data','-e','POSTGRES_USER=inventory_stock_fixture','-e','POSTGRES_PASSWORD','-e','POSTGRES_DB=inventory_stock_test','-p','127.0.0.1::5432','postgres:17-alpine'))
    # Image-pull progress may appear on stderr; accept only the container ID.
    $candidateIDs = @($runOutput | ForEach-Object { ([string]$_).Trim() } | Where-Object { $_ -match '^[0-9a-f]{64}$' })
    if ($candidateIDs.Count -ne 1) { throw 'Stock container identity output invalid' }
    $containerID = $candidateIDs[0]
    CheckOwnership
    $mapping = ([string](FixtureDocker @('port',$containerID,'5432/tcp'))).Trim()
    if ($mapping -notmatch '^127\.0\.0\.1:(\d+)$') { throw 'Unexpected fixture port binding' }
    $port = $Matches[1]
    $ready = $false
    for ($i=0; $i -lt 60; $i++) {
        try { if ((SQL 'SELECT 1') -eq '1') { $ready=$true; break } } catch { }
        Start-Sleep -Seconds 1
    }
    if (!$ready) { throw 'Stock fixture readiness failed' }
    SQL "ALTER DATABASE inventory_stock_test SET stock.fixture_id = '$run'" | Out-Null
    Migrate @('up','1')
    AssertSQL "SELECT version::text || ':' || dirty::text FROM schema_migrations" '1:false' 'warehouse baseline version'
    SQL "INSERT INTO warehouses(id,code,name,is_default) VALUES ('01935f00-0000-7000-8000-000000000001','SLICE2-BASELINE','Preserved baseline',true)" | Out-Null
    Migrate @('up','1')
    AssertSQL "SELECT version::text || ':' || dirty::text FROM schema_migrations" '2:false' 'stock up version and clean state'
    AssertSQL "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('stock_balances','stock_operations','stock_movements','stock_idempotency')" '4' 'four stock tables'
    AssertSQL "SELECT count(*) FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname LIKE 'stock_%'" '7' 'seven stock integrity/result functions'
    AssertSQL "SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal AND tgname LIKE 'stock_%'" '10' 'ten stock guards and deferred triggers'
    AssertSQL "SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND tablename LIKE 'stock_%'" '12' 'twelve stock constraint/query indexes'
    Migrate @('down','1')
    AssertSQL "SELECT version::text || ':' || dirty::text FROM schema_migrations" '1:false' 'stock down preserves version 1'
    AssertSQL "SELECT count(*) FROM warehouses WHERE code='SLICE2-BASELINE' AND is_default AND is_active" '1' 'warehouse data preserved by stock down'
    AssertSQL "SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace AND relname LIKE 'stock_%'" '0' 'stock table and index cleanup'
    AssertSQL "SELECT count(*) FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname LIKE 'stock_%'" '0' 'stock function cleanup'
    AssertSQL "SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal AND tgname LIKE 'stock_%'" '0' 'stock trigger cleanup'
    Migrate @('up','1')
    AssertSQL "SELECT version::text || ':' || dirty::text FROM schema_migrations" '2:false' 'stock reapply version and clean state'
    $env:STOCK_TEST_DATABASE_URL = "postgres://inventory_stock_fixture:$($env:POSTGRES_PASSWORD)@127.0.0.1:$port/inventory_stock_test?sslmode=disable"
    $env:STOCK_TEST_RUN_ID = $run
    $env:STOCK_TEST_PORT = $port
    # Generated bindings target the latest schema; legacy 2 up/down was checked above.
    Migrate @('up')
    AssertSQL "SELECT version::text || ':' || dirty::text FROM schema_migrations" '7:false' 'current posting schema clean'
    if ($AdjustmentsOnly) {
        Migrate @('up')
        AssertSQL "SELECT version::text || ':' || dirty::text FROM schema_migrations" '7:false' 'adjustment schema clean'
        Migrate @('down','1')
        Migrate @('up','1')
        AssertSQL "SELECT version::text || ':' || dirty::text FROM schema_migrations" '7:false' 'empty reservation down/up with adjustment baseline clean'
    }
    $testPattern = if ($AdjustmentsOnly) { '^Test(Adjustment|Opening|StockPosting|StockFreshness)' } elseif ($OpeningOnly) { '^Test(Opening|StockPosting|StockFreshness)' } elseif ($PostingOnly) { '^TestStockPosting' } else { '^TestStock(SQL|Posting|Freshness)' }
    & go -C $root test -p=1 -count=1 -timeout=180s -v ./internal/repository/postgres -run $testPattern
    if ($LASTEXITCODE -ne 0) { throw 'Focused stock SQL tests failed; fixture cleanup will run' }
    if ($AdjustmentsOnly) {
        Migrate @('down','1') # Empty fulfillment schema.
        Migrate @('down','1') # Empty reservation schema; preserved audit is version 5.
        $audit=SQL 'SELECT count(*) FROM stock_operation_audit'
        $movement=SQL 'SELECT count(*) FROM stock_movements'
        $refused=$false
        try { Migrate @('down','1') } catch { $refused=$true }
        if (!$refused) {throw 'Populated audit downgrade unexpectedly succeeded'}
        AssertSQL 'SELECT count(*) FROM stock_operation_audit' $audit 'populated downgrade preserves audit'
        AssertSQL 'SELECT count(*) FROM stock_movements' $movement 'populated downgrade preserves ledger'
        Write-Host 'PASS: adjustments, whole reversals and all posting/Opening regressions; synthetic Catalog evidence.'
        return
    }
    if ($OpeningOnly) {
        $movements=SQL 'SELECT count(*) FROM stock_movements'
        Migrate @('goto','3')
        AssertSQL 'SELECT count(*) FROM stock_movements' $movements 'opening down preserves posted history'
        Migrate @('up')
        AssertSQL "SELECT version::text || ':' || dirty::text FROM schema_migrations" '7:false' 'opening reapply clean'
        Write-Host 'PASS: opening guards, persistence, concurrency and recovery verified.'
        return
    }
    # Exercise destructive rollback with posted fixture data too, then reapply.
    Migrate @('goto','1')
    AssertSQL "SELECT count(*) FROM warehouses WHERE code='SLICE2-BASELINE'" '1' 'populated stock rollback preserves warehouse data'
    Migrate @('up')
    AssertSQL "SELECT version::text || ':' || dirty::text FROM schema_migrations" '7:false' 'populated rollback and current-schema reapply clean state'
    Write-Host 'PASS: isolated stock migration and SQL verification (synthetic Catalog references only).'
} finally {
    try {
        if ($containerAttempted -and !$containerID) {
            $owned = @(FixtureDocker @('ps','-a','--no-trunc','--filter',"label=inventory.stock.fixture=$run",'--format','{{.ID}}'))
            if ($owned.Count -gt 1) { throw 'Unexpected multiple run-owned containers; refusing ambiguous cleanup' }
            if ($owned.Count -eq 1) { $containerID=([string]$owned[0]).Trim() }
        }
        if ($containerID) {
            CheckOwnership
            FixtureDocker @('rm','-f',$containerID) | Out-Null
            $remaining = FixtureDocker @('ps','-a','--filter',"label=inventory.stock.fixture=$run",'--format','{{.ID}}')
            if ($remaining) { throw 'Stock fixture cleanup incomplete' }
            Write-Host 'PASS: run-owned PostgreSQL container removed; tmpfs data discarded.'
        }
    } finally {
        foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name,$previous[$name],'Process') }
        if (Test-Path -LiteralPath $temporary) {
            $resolved = (Resolve-Path -LiteralPath $temporary).Path
            $expected = [IO.Path]::GetFullPath((Join-Path $root ('.' + $run)))
            if ($resolved -ne $expected -or !(Split-Path $resolved -Parent).Equals($root,[StringComparison]::OrdinalIgnoreCase)) { throw 'Refusing cleanup outside run-created directory' }
            Remove-Item -LiteralPath $resolved -Recurse -Force
        }
        Write-Host 'PASS: run-created temporary files removed; previous environment restored.'
    }
}
