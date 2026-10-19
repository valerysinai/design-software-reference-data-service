[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

function New-TemporaryPassword {
    $bytes = New-Object byte[] 32
    $generator = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $generator.GetBytes($bytes)
        return [Convert]::ToBase64String($bytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
    }
    finally {
        [Array]::Clear($bytes, 0, $bytes.Length)
        $generator.Dispose()
    }
}

$repoRoot = Split-Path $PSScriptRoot -Parent
$workspaceRoot = Split-Path $repoRoot -Parent
$infraEnvPath = Join-Path $workspaceRoot "docker-infra\design-software-docker-infra\.env"
$iamHealthUrl = "http://localhost:8081/actuator/health"
$iamJwksHealthUrl = "http://localhost:8081/api/v1/auth/.well-known/jwks.json"
$referenceHealthUrl = "http://localhost:8080/health/live"
$iamBaseUrl = "http://host.docker.internal:8081"
$referenceBaseUrl = "http://host.docker.internal:8080"
$issuer = "https://iam.hu019.local"
$envFilePath = Join-Path $env:TEMP ("hu-ref-019-e2e-{0}.env" -f [guid]::NewGuid())
$sqlFilePath = Join-Path $env:TEMP ("hu-ref-019-e2e-{0}.sql" -f [guid]::NewGuid())
$adminPassword = $null
$learnerPassword = $null
$envContent = $null
$envLines = $null
$databasePassword = $null
$infraValues = $null

try {
    if (-not (Test-Path $infraEnvPath)) {
        throw "docker-infra .env was not found at $infraEnvPath."
    }

    foreach ($endpoint in @($iamHealthUrl, $iamJwksHealthUrl, $referenceHealthUrl)) {
        try {
            $response = Invoke-WebRequest -Uri $endpoint -TimeoutSec 5 -UseBasicParsing
            if ($response.StatusCode -ne 200) {
                throw "HTTP $($response.StatusCode)"
            }
        }
        catch {
            throw "Required local endpoint is unavailable: $endpoint"
        }
    }

    $infraValues = @{}
    foreach ($line in Get-Content $infraEnvPath) {
        if ($line -match '^\s*([^#=\s]+)\s*=(.*)$') {
            $infraValues[$matches[1]] = $matches[2].Trim()
        }
    }

    foreach ($required in @("POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD")) {
        if (-not $infraValues.ContainsKey($required) -or [string]::IsNullOrWhiteSpace($infraValues[$required])) {
            throw "Required database setting $required is missing from docker-infra .env."
        }
    }

    $databaseContainer = "hu019-e2e-postgres-1"
    $labelsJson = docker inspect --format '{{json .Config.Labels}}' $databaseContainer 2>$null
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($labelsJson)) {
        throw "The isolated HU-REF-019 database container '$databaseContainer' is not running."
    }
    $containerLabels = $labelsJson | ConvertFrom-Json
    if ($containerLabels.'com.docker.compose.project' -cne "hu019-e2e" -or $containerLabels.'com.docker.compose.service' -cne "postgres") {
        throw "Refusing to reset fixture passwords: the target is not the isolated hu019-e2e PostgreSQL container."
    }

    $databaseName = $infraValues["POSTGRES_DB"]
    $databaseUser = [uri]::EscapeDataString($infraValues["POSTGRES_USER"])
    $databasePassword = [uri]::EscapeDataString($infraValues["POSTGRES_PASSWORD"].Replace('$$', '$'))
    $postgresPort = "15436"
    $parsedPort = 0
    if (-not [int]::TryParse($postgresPort, [ref]$parsedPort) -or $parsedPort -lt 1 -or $parsedPort -gt 65535) {
        throw "Invalid PostgreSQL port '$postgresPort'. Enter a port between 1 and 65535, usually 15436 for HU-REF-019."
    }
    $tcpClient = [System.Net.Sockets.TcpClient]::new()
    try {
        $connect = $tcpClient.BeginConnect("localhost", $parsedPort, $null, $null)
        if (-not $connect.AsyncWaitHandle.WaitOne(3000)) {
            throw "PostgreSQL did not respond on localhost:$parsedPort."
        }
        $tcpClient.EndConnect($connect)
    }
    catch {
        throw "PostgreSQL is unavailable at localhost:$parsedPort. Start the HU-REF-019 database or enter its mapped host port."
    }
    finally {
        $tcpClient.Dispose()
    }

    $adminEmail = "hu-ref-018-admin@example.test"
    $learnerEmail = "hu-ref-018-learner@example.test"
    $fixturePassword = New-TemporaryPassword
    $adminPassword = $fixturePassword
    $learnerPassword = $fixturePassword
    $fixturePassword = $null

    $dsn = "postgres://{0}:{1}@host.docker.internal:{2}/{3}?sslmode=disable" -f $databaseUser, $databasePassword, $postgresPort, $databaseName
    $envLines = @(
        "POSTGRES_DB=$databaseName",
        "POSTGRES_USER=$($infraValues['POSTGRES_USER'])",
        "POSTGRES_PASSWORD=$($infraValues['POSTGRES_PASSWORD'].Replace('$$', '$'))",
        "REFERENCE_DATA_TEST_DSN=$dsn",
        "IAM_JWT_ISSUER=$issuer",
        "HU_REF_019_IAM_BASE_URL=$iamBaseUrl",
        "HU_REF_019_REFERENCE_DATA_BASE_URL=$referenceBaseUrl",
        "HU_REF_019_ADMIN_EMAIL=$adminEmail",
        "HU_REF_019_ADMIN_PASSWORD=$adminPassword",
        "HU_REF_019_LEARNER_EMAIL=$learnerEmail",
        "HU_REF_019_LEARNER_PASSWORD=$learnerPassword"
    )
    $envContent = $envLines -join "`n"
    [System.IO.File]::WriteAllText($envFilePath, $envContent, [System.Text.UTF8Encoding]::new($false))
    [System.IO.File]::WriteAllText($sqlFilePath, @'
\getenv fixture_password HU_REF_019_ADMIN_PASSWORD
\getenv admin_email HU_REF_019_ADMIN_EMAIL
\getenv learner_email HU_REF_019_LEARNER_EMAIL
SELECT count(*) = 2 AS fixtures_present
FROM identity."user"
WHERE email IN (:'admin_email', :'learner_email')
\gset
\if :fixtures_present
\echo Resetting passwords for isolated HU-REF-019 fixtures
\else
\echo Expected both HU-REF-019 fixture accounts; no passwords changed
\quit 1
\endif
UPDATE identity."user"
SET password_hash = crypt(:'fixture_password', gen_salt('bf', 12))
WHERE email IN (:'admin_email', :'learner_email');
'@, [System.Text.UTF8Encoding]::new($false))

    $fileAcl = Get-Acl $envFilePath
    $fileAcl.SetAccessRuleProtection($true, $false)
    foreach ($rule in @($fileAcl.Access)) {
        [void]$fileAcl.RemoveAccessRuleAll($rule)
    }
    $currentUser = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
    $userRule = [System.Security.AccessControl.FileSystemAccessRule]::new(
        $currentUser,
        [System.Security.AccessControl.FileSystemRights]::FullControl,
        [System.Security.AccessControl.AccessControlType]::Allow
    )
    [void]$fileAcl.AddAccessRule($userRule)
    Set-Acl -Path $envFilePath -AclObject $fileAcl

    docker run --rm `
        --network hu019-e2e_default `
        --env-file $envFilePath `
        -v "${sqlFilePath}:/tmp/reset-fixtures.sql:ro" `
        postgres:16-alpine `
        sh -c 'export PGPASSWORD="$POSTGRES_PASSWORD"; exec psql -h postgres -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -f /tmp/reset-fixtures.sql'
    if ($LASTEXITCODE -ne 0) {
        throw "Could not reset the fixture passwords in the isolated E2E database."
    }

    docker run --rm `
        -v "${repoRoot}:/workspace" `
        -w /workspace `
        --env-file $envFilePath `
        golang:1.26.5 `
        sh -c "go test -count=1 -v ./internal/adapter/in/http/hierarchy -run TestHURef019ParametersWithRealIAMJWT"
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
}
finally {
    if ($null -ne $adminPassword) { $adminPassword = $null }
    if ($null -ne $learnerPassword) { $learnerPassword = $null }
    if ($null -ne $fixturePassword) { $fixturePassword = $null }
    if ($null -ne $envContent) { $envContent = $null }
    if ($null -ne $envLines) { $envLines = $null }
    if ($null -ne $databasePassword) { $databasePassword = $null }
    if ($null -ne $infraValues -and $infraValues.ContainsKey("POSTGRES_PASSWORD")) { $infraValues["POSTGRES_PASSWORD"] = $null }
    if (Test-Path $envFilePath) { Remove-Item -LiteralPath $envFilePath -Force }
    if (Test-Path $sqlFilePath) { Remove-Item -LiteralPath $sqlFilePath -Force }
}