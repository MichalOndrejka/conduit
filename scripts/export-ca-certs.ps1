# Exports every trusted root CA from the Windows certificate store into
# ./certs as a single PEM bundle, so the containers can trust an internal CA
# or a corporate TLS-interception proxy without manually hunting for the
# right cert in certmgr.msc. docker-compose.yml mounts the folder named by
# CONDUIT_CERTS_DIR (default ./certs) into ollama (model pulls) and conduit
# (source syncs); for a standalone container:
#   docker run -v ./certs:/certs:ro -e SSL_CERT_DIR=/certs ...
# Re-run this whenever HTTPS calls start failing with an x509 verification
# error (e.g. the corporate cert rotated).
#
# Run from a clone it writes the repo's certs/ folder. Run without a clone,
# it writes .\certs in the current folder:
#   irm https://raw.githubusercontent.com/MichalOndrejka/conduit/main/scripts/export-ca-certs.ps1 | iex
$ErrorActionPreference = "Stop"

# $PSScriptRoot is empty when piped into iex — there is no script file.
if ($PSScriptRoot) {
    $outDir = Join-Path $PSScriptRoot "..\certs"
} else {
    $outDir = Join-Path (Get-Location) "certs"
}
New-Item -ItemType Directory -Force -Path $outDir | Out-Null
$outFile = Join-Path $outDir "windows-root-ca.pem"

$stores = @(
    (Get-ChildItem -Path Cert:\LocalMachine\Root),
    (Get-ChildItem -Path Cert:\CurrentUser\Root)
)

$lines = foreach ($store in $stores) {
    foreach ($cert in $store) {
        "-----BEGIN CERTIFICATE-----"
        [Convert]::ToBase64String($cert.RawData, [System.Base64FormattingOptions]::InsertLineBreaks)
        "-----END CERTIFICATE-----"
    }
}

$lines | Set-Content -Path $outFile -Encoding ascii
Write-Host "Wrote $(($stores | ForEach-Object { $_.Count } | Measure-Object -Sum).Sum) certs to $outFile"

$certsDir = (Resolve-Path $outDir).Path
Write-Host ""
Write-Host "Point docker compose at it in this shell (or put CONDUIT_CERTS_DIR in .env):"
Write-Host "  `$env:CONDUIT_CERTS_DIR = `"$certsDir`""
