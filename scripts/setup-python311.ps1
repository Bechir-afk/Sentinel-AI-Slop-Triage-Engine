$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent $PSScriptRoot
$venvPath = Join-Path $repoRoot '.venv-ml'
$venvPython = Join-Path $venvPath 'Scripts\python.exe'

if (-not (Test-Path -LiteralPath $venvPython)) {
    py -3.11 -m venv $venvPath
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

. (Join-Path $venvPath 'Scripts\Activate.ps1')
& $venvPython -c "import sys; assert sys.version_info[:2] == (3, 11), 'Expected Python 3.11'"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# PyPI's Windows wheel is CPU-only; install the matching CUDA build first.
& $venvPython -m pip install --no-deps --only-binary=:all: 'torch==2.5.1+cu124' --index-url 'https://download.pytorch.org/whl/cu124'
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

& $venvPython -m pip install --only-binary=:all: -r (Join-Path $repoRoot 'ml\requirements.txt')
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

& $venvPython -c "import torch; assert torch.cuda.is_available(), 'CUDA is unavailable'; print(f'CUDA ready: {torch.cuda.get_device_name(0)} ({torch.__version__})')"
exit $LASTEXITCODE
