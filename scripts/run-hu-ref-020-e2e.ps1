[CmdletBinding()]
param([switch]$PrepareOnly)
$ErrorActionPreference = 'Stop'
$workspaceRoot = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$runner = Join-Path $workspaceRoot 'docker-infra/design-software-docker-infra/validation/hu-ref-020/run.ps1'
if (-not (Test-Path $runner)) {
    throw 'Clone docker-infra with the HU-REF-020 integration runner next to the service repositories. See docs/HU-REF-020-VALIDACION.md.'
}
& $runner -PrepareOnly:$PrepareOnly
