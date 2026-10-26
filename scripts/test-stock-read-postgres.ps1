#Requires -Version 7.4
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
$workspace=Split-Path $root -Parent
$run='inventory-stock-test-'+[guid]::NewGuid().ToString('N')
$container=$null;$attempted=$false
$names=@('POSTGRES_PASSWORD','PGPASSWORD','STOCK_TEST_DATABASE_URL','STOCK_TEST_RUN_ID','STOCK_TEST_PORT','STOCK_READ_ISOLATED')
$previous=@{};foreach($name in $names){$previous[$name]=[Environment]::GetEnvironmentVariable($name,'Process')}
function FixtureDocker([string[]]$Arguments){$result=& docker @Arguments 2>&1;if($LASTEXITCODE -ne 0){throw "Stock read fixture Docker operation failed ($($Arguments[0])); raw output suppressed"};return $result}
function Owned{if(!$container -or $container -notmatch '^[0-9a-f]{64}$'){throw 'Missing fixture identity'};$label=([string](FixtureDocker @('inspect','--format','{{index .Config.Labels "inventory.read.fixture"}}',$container))).Trim();if($label -ne $run){throw 'Refusing non-owned fixture operation'}}
function SQL([string]$query,[string]$database='inventory_stock_test'){Owned;$result=$query|& docker exec -i $container psql -X -q -t -A -v ON_ERROR_STOP=1 -U inventory_stock_fixture -d $database 2>&1;if($LASTEXITCODE -ne 0){throw 'Fixture SQL failed; raw output suppressed'};return ([string]::Join("`n",[string[]]@($result))).Trim()}
function AssertSQL([string]$query,[string]$expected,[string]$label,[string]$database='inventory_stock_test'){if((SQL $query $database) -ne $expected){throw "Fixture assertion failed: $label"};Write-Host "PASS: $label"}
function Migrate([string]$repository,[string]$database,[string[]]$operation){Owned;$path=Join-Path (Join-Path $workspace $repository) 'db/migrations';FixtureDocker (@('run','--rm','--network',"container:$container",'-e','PGPASSWORD','-v',"${path}:/migrations:ro",'migrate/migrate:v4.18.3','-path=/migrations',"-database=postgres://inventory_stock_fixture@127.0.0.1:5432/${database}?sslmode=disable")+$operation)|Out-Null}
try{
 $bytes=[byte[]]::new(32);[Security.Cryptography.RandomNumberGenerator]::Fill($bytes);$env:POSTGRES_PASSWORD=[Convert]::ToHexString($bytes);$env:PGPASSWORD=$env:POSTGRES_PASSWORD
 $attempted=$true;$output=@(FixtureDocker @('run','--rm','-d','--name',$run,'--label',"inventory.read.fixture=$run",'--tmpfs','/var/lib/postgresql/data','-e','POSTGRES_USER=inventory_stock_fixture','-e','POSTGRES_PASSWORD','-e','POSTGRES_DB=inventory_stock_test','-p','127.0.0.1::5432','postgres:17-alpine'));$ids=@($output|ForEach-Object{([string]$_).Trim()}|Where-Object{$_ -match '^[0-9a-f]{64}$'});if($ids.Count -ne 1){throw 'Invalid fixture ID'};$container=$ids[0];Owned
 $mapping=([string](FixtureDocker @('port',$container,'5432/tcp'))).Trim();if($mapping -notmatch '^127\.0\.0\.1:(\d+)$'){throw 'Invalid fixture port'};$port=$Matches[1]
 $ready=$false;for($i=0;$i -lt 60;$i++){try{if((SQL 'SELECT 1') -eq '1'){$ready=$true;break}}catch{};Start-Sleep -Seconds 1};if(!$ready){throw 'Fixture readiness failed'}
 SQL "ALTER DATABASE inventory_stock_test SET stock.fixture_id='$run'"|Out-Null
 Migrate 'Inventory-Service' 'inventory_stock_test' @('up','3')
 AssertSQL "SELECT version::text||':'||dirty::text FROM schema_migrations" '3:false' 'Inventory schema version 3'
 SQL "INSERT INTO warehouses(id,code,name,is_default) VALUES ('01935f00-0000-7000-8000-000000000001','READ-DEFAULT','Synthetic default',true)"|Out-Null
 SQL 'CREATE DATABASE auth_stock_read_test'|Out-Null
 Migrate 'Authentication' 'auth_stock_read_test' @('up','7')
 SQL "INSERT INTO identity_permissions(resource,action,description) VALUES ('inventory.stock-balances','read','Preserved synthetic definition')" 'auth_stock_read_test'|Out-Null
 Migrate 'Authentication' 'auth_stock_read_test' @('up','1')
 AssertSQL "SELECT count(*) FROM identity_permissions WHERE action='read' AND resource IN ('inventory.stock-balances','inventory.stock-movements')" '2' 'Auth definitions exist' 'auth_stock_read_test'
 AssertSQL "SELECT count(*) FROM identity_permissions WHERE resource='inventory.stock-balances' AND description='Preserved synthetic definition'" '1' 'pre-existing permission preserved' 'auth_stock_read_test'
 AssertSQL 'SELECT (SELECT count(*) FROM identity_role_permissions)+(SELECT count(*) FROM identity_user_roles)' '0' 'no automatic user/role grants' 'auth_stock_read_test'
 Migrate 'Authentication' 'auth_stock_read_test' @('down','1')
 AssertSQL "SELECT count(*) FROM identity_permissions WHERE action='read' AND resource IN ('inventory.stock-balances','inventory.stock-movements')" '2' 'Auth down retains definitions' 'auth_stock_read_test'
 Migrate 'Authentication' 'auth_stock_read_test' @('up','1')
 AssertSQL "SELECT version::text||':'||dirty::text FROM schema_migrations" '8:false' 'Auth permission down/reapply clean' 'auth_stock_read_test'
 $env:STOCK_TEST_DATABASE_URL="postgres://inventory_stock_fixture:$($env:POSTGRES_PASSWORD)@127.0.0.1:$port/inventory_stock_test?sslmode=disable";$env:STOCK_TEST_RUN_ID=$run;$env:STOCK_TEST_PORT=$port;$env:STOCK_READ_ISOLATED='1'
 & go -C $root test -count=1 -timeout=240s -v ./internal/repository/postgres -run '^TestStockRead'
 if($LASTEXITCODE -ne 0){throw 'Stock read disposable tests failed'}
 $count=SQL 'SELECT count(*) FROM stock_movements'
 Migrate 'Inventory-Service' 'inventory_stock_test' @('down','1')
 AssertSQL "SELECT count(*) FROM pg_indexes WHERE indexname IN ('stock_movements_warehouse_id_desc','stock_movements_warehouse_variant_id_desc')" '0' 'index rollback removed only new indexes'
 AssertSQL 'SELECT count(*) FROM stock_movements' $count 'index rollback preserved ledger'
 Migrate 'Inventory-Service' 'inventory_stock_test' @('up','1')
 AssertSQL "SELECT count(*) FROM pg_indexes WHERE indexname IN ('stock_movements_warehouse_id_desc','stock_movements_warehouse_variant_id_desc')" '2' 'index reapply complete'
 Write-Host 'PASS: disposable stock read verification complete'
}finally{
 try{if($attempted -and !$container){$owned=@(FixtureDocker @('ps','-a','--no-trunc','--filter',"label=inventory.read.fixture=$run",'--format','{{.ID}}'));if($owned.Count -gt 1){throw 'Ambiguous fixture cleanup'};if($owned.Count -eq 1){$container=([string]$owned[0]).Trim()}};if($container){Owned;FixtureDocker @('rm','-f',$container)|Out-Null;$remaining=FixtureDocker @('ps','-a','--filter',"label=inventory.read.fixture=$run",'--format','{{.ID}}');if($remaining){throw 'Fixture cleanup incomplete'};Write-Host 'PASS: owned container/tmpfs removed'}}finally{foreach($name in $names){[Environment]::SetEnvironmentVariable($name,$previous[$name],'Process')};Write-Host 'PASS: previous environment restored'}
}
