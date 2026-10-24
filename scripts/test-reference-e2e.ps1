#Requires -Version 7.4
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
$workspace=Split-Path $root -Parent
$id=[guid]::NewGuid().ToString('N')
$run='stock-reference-e2e-'+$id
$container=$null;$attempted=$false
$names=@('POSTGRES_PASSWORD','PGPASSWORD','AUTH_MACHINE_TEST_DATABASE_URL','AUTH_MACHINE_TEST_RUN_ID','AUTH_MACHINE_TEST_PORT','CATALOG_STOCK_TEST_DATABASE_URL','CATALOG_STOCK_TEST_RUN_ID','CATALOG_STOCK_TEST_PORT','STOCK_TEST_DATABASE_URL','STOCK_TEST_RUN_ID','STOCK_TEST_PORT','STOCK_E2E_ENABLED')
$previous=@{};foreach($name in $names){$previous[$name]=[Environment]::GetEnvironmentVariable($name,'Process')}
function FixtureDocker([string[]]$Arguments){$output=& docker @Arguments 2>&1;if($LASTEXITCODE -ne 0){throw "Owned E2E Docker operation failed ($($Arguments[0])); raw output suppressed"};return $output}
function Owned{if(!$container -or $container -notmatch '^[0-9a-f]{64}$'){throw 'Missing fixture identity'};$label=([string](FixtureDocker @('inspect','--format','{{index .Config.Labels "stock.reference.fixture"}}',$container))).Trim();if($label -ne $run){throw 'Fixture ownership mismatch'}}
function SQL([string]$query){Owned;$output=$query | & docker exec -i $container psql -X -q -t -A -v ON_ERROR_STOP=1 -U auth_machine_fixture -d auth_machine_test 2>&1;if($LASTEXITCODE -ne 0){throw 'Fixture SQL failed; output suppressed'};return ([string]::Join("`n",[string[]]@($output))).Trim()}
function Migrate([string]$repo,[string]$role,[string]$database){Owned;$path=Join-Path (Join-Path $workspace $repo) 'db/migrations';FixtureDocker @('run','--rm','--network',"container:$container",'-e','PGPASSWORD','-v',"${path}:/migrations:ro",'migrate/migrate:v4.18.3','-path=/migrations',"-database=postgres://${role}@127.0.0.1:5432/${database}?sslmode=disable",'up')|Out-Null}
try{
 $random=[byte[]]::new(32);[Security.Cryptography.RandomNumberGenerator]::Fill($random);$env:POSTGRES_PASSWORD=[Convert]::ToHexString($random);$env:PGPASSWORD=$env:POSTGRES_PASSWORD
 $attempted=$true;$output=@(FixtureDocker @('run','--rm','-d','--name',$run,'--label',"stock.reference.fixture=$run",'--tmpfs','/var/lib/postgresql/data','-e','POSTGRES_USER=auth_machine_fixture','-e','POSTGRES_PASSWORD','-e','POSTGRES_DB=auth_machine_test','-p','127.0.0.1::5432','postgres:17-alpine'));$ids=@($output|ForEach-Object{([string]$_).Trim()}|Where-Object{$_ -match '^[0-9a-f]{64}$'});if($ids.Count -ne 1){throw 'Invalid fixture id'};$container=$ids[0];Owned
 $mapping=([string](FixtureDocker @('port',$container,'5432/tcp'))).Trim();if($mapping -notmatch '^127\.0\.0\.1:(\d+)$'){throw 'Invalid fixture port'};$port=$Matches[1]
 $ready=$false;for($i=0;$i -lt 60;$i++){try{if((SQL 'SELECT 1') -eq '1'){$ready=$true;break}}catch{};Start-Sleep -Seconds 1};if(!$ready){throw 'Fixture not ready'}
 SQL "CREATE ROLE catalog_stock_fixture LOGIN PASSWORD '$($env:POSTGRES_PASSWORD)'; CREATE ROLE inventory_stock_fixture LOGIN PASSWORD '$($env:POSTGRES_PASSWORD)'"|Out-Null
 SQL 'CREATE DATABASE catalog_stock_test OWNER catalog_stock_fixture'|Out-Null
 SQL 'CREATE DATABASE inventory_stock_test OWNER inventory_stock_fixture'|Out-Null
 SQL "ALTER DATABASE auth_machine_test SET auth.fixture_id='auth-machine-test-$id'; ALTER DATABASE catalog_stock_test SET catalog.fixture_id='catalog-stock-test-$id'; ALTER DATABASE inventory_stock_test SET stock.fixture_id='inventory-stock-test-$id'"|Out-Null
 Migrate 'Authentication' 'auth_machine_fixture' 'auth_machine_test';Migrate 'Catalog-Service' 'catalog_stock_fixture' 'catalog_stock_test';Migrate 'Inventory-Service' 'inventory_stock_fixture' 'inventory_stock_test'
 FixtureDocker @('exec',$container,'psql','-X','-v','ON_ERROR_STOP=1','-U','inventory_stock_fixture','-d','inventory_stock_test','-c',"INSERT INTO warehouses(id,code,name,is_default) VALUES ('01935f00-0000-7000-8000-000000000001','E2E-DEFAULT','Synthetic default',true)")|Out-Null
 $env:AUTH_MACHINE_TEST_DATABASE_URL="postgres://auth_machine_fixture:$($env:POSTGRES_PASSWORD)@127.0.0.1:$port/auth_machine_test?sslmode=disable";$env:AUTH_MACHINE_TEST_RUN_ID="auth-machine-test-$id";$env:AUTH_MACHINE_TEST_PORT=$port
 $env:CATALOG_STOCK_TEST_DATABASE_URL="postgres://catalog_stock_fixture:$($env:POSTGRES_PASSWORD)@127.0.0.1:$port/catalog_stock_test?sslmode=disable";$env:CATALOG_STOCK_TEST_RUN_ID="catalog-stock-test-$id";$env:CATALOG_STOCK_TEST_PORT=$port
 $env:STOCK_TEST_DATABASE_URL="postgres://inventory_stock_fixture:$($env:POSTGRES_PASSWORD)@127.0.0.1:$port/inventory_stock_test?sslmode=disable";$env:STOCK_TEST_RUN_ID="inventory-stock-test-$id";$env:STOCK_TEST_PORT=$port;$env:STOCK_E2E_ENABLED='1'
 & go -C $root test -count=1 -timeout=240s -v ./internal/repository/postgres -run '^TestStock(HTTPSPostingE2E|FreshnessAfterPoolWait)$'
 if($LASTEXITCODE -ne 0){throw 'Disposable HTTPS posting verification failed'}
 Write-Host 'PASS: three owned databases; actual HTTPS issuer/reference/posting verified'
}finally{
 try{if($attempted -and !$container){$owned=@(FixtureDocker @('ps','-a','--no-trunc','--filter',"label=stock.reference.fixture=$run",'--format','{{.ID}}'));if($owned.Count -gt 1){throw 'Ambiguous fixture cleanup'};if($owned.Count -eq 1){$container=([string]$owned[0]).Trim()}};if($container){Owned;FixtureDocker @('rm','-f',$container)|Out-Null;Write-Host 'PASS: owned container/tmpfs removed'}}finally{foreach($name in $names){[Environment]::SetEnvironmentVariable($name,$previous[$name],'Process')};Write-Host 'PASS: prior environment restored'}
}
