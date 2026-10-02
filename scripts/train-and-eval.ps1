[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^[^,\s/]+/[^,\s/]+(?:,[^,\s/]+/[^,\s/]+)*$')]
    [string]$Repositories
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent $PSScriptRoot
$mlDirectory = Join-Path $repoRoot 'ml'
$venvPython = Join-Path $repoRoot '.venv-ml\Scripts\python.exe'
$artifactDirectory = Join-Path $repoRoot 'model\model'

if (-not (Test-Path -LiteralPath $venvPython -PathType Leaf)) {
    throw "Python environment not found. Run scripts\setup-python311.ps1 first."
}
if (-not $env:GITHUB_TOKEN) {
    throw 'GITHUB_TOKEN is required for PR collection.'
}

function Invoke-PipelineStep {
    param([string[]]$Arguments)

    & $venvPython @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$($Arguments[0]) failed with exit code $LASTEXITCODE."
    }
}

Push-Location $mlDirectory
try {
    Invoke-PipelineStep -Arguments @('collect_prs.py', '--repos', $Repositories, '--out', 'raw_prs.jsonl', '--per-repo', '200')
    Invoke-PipelineStep -Arguments @('build_dataset.py', '--raw', 'raw_prs.jsonl', '--synthetic', '800', '--out', 'dataset')
    Invoke-PipelineStep -Arguments @('train.py', '--data', 'dataset', '--out', $artifactDirectory, '--epochs', '3', '--batch-size', '4', '--lr', '2e-5', '--bar', '0.85')

    $requiredArtifactFiles = @('config.json', 'model.safetensors', 'tokenizer_config.json', 'tokenizer.json', 'vocab.json', 'threshold.json')
    $missingArtifactFiles = $requiredArtifactFiles | Where-Object {
        -not (Test-Path -LiteralPath (Join-Path $artifactDirectory $_) -PathType Leaf)
    }
    if ($missingArtifactFiles) {
        throw "Artifact is incomplete. Missing: $($missingArtifactFiles -join ', ')."
    }
    if (Test-Path -LiteralPath (Join-Path $artifactDirectory 'pytorch_model.bin') -PathType Leaf) {
        throw 'Unsafe legacy model weights found: pytorch_model.bin.'
    }

    $thresholdRecord = Get-Content -LiteralPath (Join-Path $artifactDirectory 'threshold.json') -Raw | ConvertFrom-Json
    $thresholdFields = $thresholdRecord.PSObject.Properties.Name
    $missingThresholdFields = @('threshold', 'val_precision', 'bar') | Where-Object { $_ -notin $thresholdFields }
    if ($missingThresholdFields) {
        throw "threshold.json is incomplete. Missing: $($missingThresholdFields -join ', ')."
    }

    Invoke-PipelineStep -Arguments @('evaluate.py', '--model', $artifactDirectory, '--test', 'dataset\test.parquet', '--bar', '0.85')
}
finally {
    Pop-Location
}
