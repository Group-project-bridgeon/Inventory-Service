#Requires -Version 7.4
[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$run = 'inventory-stock-test-' + [guid]::NewGuid().ToString('N')
$temporary = Join-Path $root ('.' + $run)
$containerID = $null
$containerAttempted = $false
$names = @('POSTGRES_PASSWORD','PGPASSWORD','STOCK_TEST_DATABASE_URL','STOCK_TEST_RUN_ID','STOCK_TEST_PORT','GOTMPDIR','RESERVATION_TEST_PHASE','GOMAXPROCS','RESERVATION_AUTH_DATABASE_URL','RESERVATION_AUTH_RUN_ID')
$previous = @{}
foreach ($name in $names) { $previous[$name] = [Environment]::GetEnvironmentVariable($name,'Process') }
function FixtureDocker([string[]]$Arguments) {
    $output = & docker @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw "Stock fixture Docker operation failed ($($Arguments[0])); raw output suppressed." }
    return $output
}
function CheckOwnership {
    if (!$containerID -or $containerID -notmatch '^[0-9a-f]{64}$') { throw 'No run-owned container identity' }
    $label = [string](FixtureDocker @('inspect','--format','{{index .Config.Labels "inventory.reservation.fixture"}}',$containerID))
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
function AuthMigrate([string[]]$Operation) {
    CheckOwnership
    $source=Join-Path (Split-Path $root -Parent) 'Authentication/db/migrations'
    FixtureDocker (@('run','--rm','--network',"container:$containerID",'-e','PGPASSWORD','-v',"${source}:/migrations:ro",'migrate/migrate:v4.18.3','-path=/migrations','-database=postgres://inventory_stock_fixture@127.0.0.1:5432/auth_reservation_test?sslmode=disable')+$Operation)|Out-Null
}
function GoCheck([string]$Pattern) {
    & go -C $root test -p=1 -count=1 -timeout=240s -v ./internal/repository/postgres -run $Pattern
    if ($LASTEXITCODE -ne 0) {throw 'Reservation PostgreSQL verification failed; owned cleanup will run'}
}
try {
    $null=New-Item -ItemType Directory -Path $temporary
    $env:GOTMPDIR=$temporary
    $env:GOMAXPROCS='2'
    $bytes=[byte[]]::new(32);[Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    $env:POSTGRES_PASSWORD=[Convert]::ToHexString($bytes).ToLowerInvariant();$env:PGPASSWORD=$env:POSTGRES_PASSWORD
    $containerAttempted=$true
    $output=@(FixtureDocker @('run','--rm','-d','--name',$run,'--label',"inventory.reservation.fixture=$run",'--tmpfs','/var/lib/postgresql/data','-e','POSTGRES_USER=inventory_stock_fixture','-e','POSTGRES_PASSWORD','-e','POSTGRES_DB=inventory_stock_test','-p','127.0.0.1::5432','postgres:17-alpine'))
    $ids=@($output|ForEach-Object{([string]$_).Trim()}|Where-Object{$_ -match '^[0-9a-f]{64}$'})
    if($ids.Count -ne 1){throw 'Invalid reservation fixture identity'};$containerID=$ids[0];CheckOwnership
    $mapping=([string](FixtureDocker @('port',$containerID,'5432/tcp'))).Trim()
    if($mapping -notmatch '^127\.0\.0\.1:(\d+)$'){throw 'Unexpected reservation fixture port'};$port=$Matches[1]
    $ready=$false;for($i=0;$i -lt 60;$i++){try{if((SQL 'SELECT 1') -eq '1'){$ready=$true;break}}catch{};Start-Sleep -Seconds 1}
    if(!$ready){throw 'Reservation fixture readiness failed'}
    SQL "ALTER DATABASE inventory_stock_test SET stock.fixture_id='$run'"|Out-Null
    Migrate @('goto','5')
    SQL "INSERT INTO warehouses(id,code,name,is_default) VALUES('01935f00-0000-7000-8000-000000000001','RESERVATION-DEFAULT','Fixture default',true)"|Out-Null
    $env:STOCK_TEST_DATABASE_URL="postgres://inventory_stock_fixture:$($env:POSTGRES_PASSWORD)@127.0.0.1:$port/inventory_stock_test?sslmode=disable"
    $env:STOCK_TEST_RUN_ID=$run;$env:STOCK_TEST_PORT=$port
    $env:RESERVATION_TEST_PHASE='seed'
    GoCheck '^TestReservationLegacySeed$'
    Migrate @('up','2')
    AssertSQL "SELECT version::text||':'||dirty::text FROM schema_migrations" '7:false' 'current reservation/fulfillment schema clean'
    AssertSQL "SELECT count(*) FROM stock_operations WHERE actor_kind='HUMAN' AND actor_user_id IS NOT NULL AND actor_machine_id IS NULL" '1' 'legacy human actor preserved'
    Migrate @('down','1')
    AssertSQL 'SELECT count(*) FROM stock_movements' '1' 'empty reservation downgrade preserves legacy ledger'
    Migrate @('up','1')
    $env:RESERVATION_TEST_PHASE='verify'
    GoCheck '^Test(Reservation|StockPosting|Opening|Adjustment)'
    SQL 'CREATE DATABASE auth_reservation_test'|Out-Null
    CheckOwnership
    FixtureDocker @('exec',$containerID,'psql','-X','-q','-v','ON_ERROR_STOP=1','-U','inventory_stock_fixture','-d','auth_reservation_test','-c',"ALTER DATABASE auth_reservation_test SET stock.fixture_id='$run'")|Out-Null
    AuthMigrate @('up')
    $env:RESERVATION_AUTH_DATABASE_URL="postgres://inventory_stock_fixture:$($env:POSTGRES_PASSWORD)@127.0.0.1:$port/auth_reservation_test?sslmode=disable"
    $env:RESERVATION_AUTH_RUN_ID=$run
    $authRoot=Join-Path (Split-Path $root -Parent) 'Authentication'
    & go -C $authRoot test -p=1 -count=1 -timeout=90s -v ./internal/integration -run '^TestReservationMachineAuthority$'
    if($LASTEXITCODE -ne 0){throw 'Scoped machine authority lifecycle verification failed'}
    Migrate @('down','1') # Empty fulfillment schema; reservation history remains on version 6.
    $before=SQL "SELECT concat_ws(':',(SELECT count(*) FROM stock_reservations),(SELECT count(*) FROM reservation_transitions),(SELECT count(*) FROM stock_movements),(SELECT count(*) FROM reservation_commands))"
    $refused=$false;try{Migrate @('down','1')}catch{$refused=$true}
    if(!$refused){throw 'Populated reservation downgrade must refuse'}
    AssertSQL "SELECT concat_ws(':',(SELECT count(*) FROM stock_reservations),(SELECT count(*) FROM reservation_transitions),(SELECT count(*) FROM stock_movements),(SELECT count(*) FROM reservation_commands))" $before 'populated downgrade preserves reservation and stock facts'
    Write-Host 'PASS: real PostgreSQL reservations, posting/adjustment/read compatibility, races, rollback and simulated recovery.'
} finally {
    try {
        if ($containerAttempted -and !$containerID) {
            $owned = @(FixtureDocker @('ps','-a','--no-trunc','--filter',"label=inventory.reservation.fixture=$run",'--format','{{.ID}}'))
            if ($owned.Count -gt 1) { throw 'Unexpected multiple run-owned containers; refusing ambiguous cleanup' }
            if ($owned.Count -eq 1) { $containerID=([string]$owned[0]).Trim() }
        }
        if ($containerID) {
            CheckOwnership
            FixtureDocker @('rm','-f',$containerID) | Out-Null
            $remaining = FixtureDocker @('ps','-a','--filter',"label=inventory.reservation.fixture=$run",'--format','{{.ID}}')
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
