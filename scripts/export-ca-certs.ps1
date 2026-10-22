# Exports every trusted root CA from the Windows certificate store into
# ./certs as a single PEM bundle, so the containers can trust an internal CA
# or a corporate TLS-interception proxy without manually hunting for the
# right cert in certmgr.msc. docker-compose.yml mounts ./certs into ollama
# (model pulls) and conduit (source syncs); for a standalone container:
#   docker run -v ./certs:/certs:ro -e SSL_CERT_DIR=/certs ...
# Re-run this whenever HTTPS calls start failing with an x509 verification
# error (e.g. the corporate cert rotated).
$ErrorActionPreference = "Stop"

$outDir = Join-Path $PSScriptRoot "..\certs"
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
