# check.ps1 - Windows bit-exact passthrough check for Fplay.
# Reads ~/.config/flacplayer/state.json (current song + device) itself, then compares
# the FLAC's rate/bit-depth against the audio device's shared mix format (GetMixFormat)
# to see if it was resampled / bit-dropped.
#
#   Run from the repo root (after Fplay has played at least one track):
#       powershell -File ./check.ps1
#
# NOTE: ASCII-only on purpose - Windows PowerShell 5.1 reads .ps1 with the ANSI codepage,
#       so non-ASCII (Chinese) text in the script itself would break parsing. The tool reads
#       the (possibly Chinese) filename from state.json directly, so no path crosses the
#       PowerShell -> native-arg boundary (which would mangle the encoding).
$ErrorActionPreference = "Stop"

$bin = Join-Path $PWD "flaccheck.exe"
if (-not (Test-Path $bin)) {
  Write-Host "Building check tool -> $bin"
  go build -o $bin ./tools/check
}

& $bin
exit $LASTEXITCODE
