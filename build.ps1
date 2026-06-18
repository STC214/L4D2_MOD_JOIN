$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$app = Join-Path $root "cmd\l4d2modjoin"
$dist = Join-Path $root "dist"
$tmp = Join-Path $root ".tmp"

New-Item -ItemType Directory -Force $dist | Out-Null
New-Item -ItemType Directory -Force (Join-Path $tmp "go-build") | Out-Null
New-Item -ItemType Directory -Force (Join-Path $tmp "go-cache") | Out-Null

$env:GOTMPDIR = (Resolve-Path (Join-Path $tmp "go-build")).Path
$env:GOCACHE = (Resolve-Path (Join-Path $tmp "go-cache")).Path

python (Join-Path $root "tools\build_icon.py")

Push-Location $app
try {
    rsrc -manifest app.manifest -ico (Join-Path $root "assets\app.ico") -arch amd64 -o rsrc_windows_amd64.syso
}
finally {
    Pop-Location
}

go test ./...
go build -trimpath -ldflags="-H windowsgui -s -w" -o (Join-Path $dist "L4D2ModJoin.exe") ./cmd/l4d2modjoin

Write-Host "Built: $(Join-Path $dist 'L4D2ModJoin.exe')"
