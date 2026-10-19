[CmdletBinding()]
param([switch]$PrepareOnly)
$ErrorActionPreference='Stop'
$workspaceRoot=Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$runner=Join-Path $workspaceRoot 'docker-infra/design-software-docker-infra/validation/r1-reference/run.ps1'
if (-not (Test-Path $runner)) { throw 'The R1 docker-infra runner is required next to the service repositories.' }
& $runner -PrepareOnly:$PrepareOnly
