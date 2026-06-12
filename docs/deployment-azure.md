# Deploying to Azure Container Apps

This guide deploys Conduit to Azure using Azure Container Apps (ACA), Azure OpenAI for
embeddings, and Qdrant as a second internal-only container app.

## Architecture

```
                         ┌─────────────────────────────────────────────┐
                         │       Container Apps Environment             │
                         │                                               │
  Internet ──HTTPS───────┼──▶ conduit (external ingress, port 8000)     │
                         │        │                                      │
                         │        │ HTTPS, internal FQDN, port 443       │
                         │        ▼                                      │
                         │     qdrant (internal-only ingress, 6333)      │
                         │        │                                      │
                         └────────┼──────────────────────────────────────┘
                                  ▼
                         Azure Files share "qdrant-storage"
                         mounted at /qdrant/storage

  conduit's /data  ───▶  Azure Files share "conduit-data"
  (sources, sync state, encrypted credential store)

  conduit ──HTTPS──▶ Azure OpenAI (text-embedding-3-small deployment)
```

Key decisions baked into `infra/main.bicep`:

- **Qdrant has no public endpoint.** Ingress is `external: false`, so it's only
  reachable from other apps in the same Container Apps environment, via its
  internal FQDN (`https://<app>.internal.<env-domain>`, port 443).
- **Qdrant's collection data survives restarts and scale events.** ACA's local
  container disk is ephemeral — `/qdrant/storage` is mounted from an Azure
  Files share.
- **`minReplicas: 1` on Qdrant** so it never scales to zero and loses its
  in-memory index/cache between requests.
- **Conduit's `/data` (source definitions, sync state, encrypted credential
  store) is also on Azure Files**, so it persists across redeploys.
- **Embeddings use Azure OpenAI `text-embedding-3-small` (1536 dimensions)**
  by default — cheap and good retrieval quality. The deployed dimension
  **must match** `embeddingDimensions`; if you switch to
  `text-embedding-3-large`, set `embeddingDimensions=3072` (and expect ~6x the
  cost and roughly double Qdrant's storage/RAM footprint).
- **Secrets are native Container Apps secrets** (the Azure OpenAI key, a
  generated Qdrant API key, and a generated Conduit API key), referenced via
  `secretRef` — no Key Vault required.
- **Two-track auth (Go backend).** Conduit's ingress is public
  (`external: true`), so every request is gated: browsers sign in with the
  **owner account** (n8n-style login — seed it via the `conduitOwnerEmail` /
  `conduitOwnerPassword` parameters, or complete the first-run `/setup` page),
  while headless MCP/API clients authenticate with the generated
  `CONDUIT_API_KEY` bearer secret. See
  [Securing the deployment](#securing-the-deployment).

## Prerequisites

- An Azure subscription with access to Azure OpenAI (`Microsoft.CognitiveServices`
  provider registered, and quota for `text-embedding-3-small` in your chosen
  region — `eastus` and `swedencentral` are good bets).
- [Azure CLI](https://learn.microsoft.com/cli/azure/install-azure-cli) with the
  Container Apps extension: `az extension add --name containerapp --upgrade`.
- A resource group to deploy into.

## 1. Deploy the infrastructure

```bash
az group create --name rg-conduit --location westeurope

az deployment group create \
  --resource-group rg-conduit \
  --template-file infra/main.bicep \
  --parameters infra/main.parameters.json
```

Adjust `infra/main.parameters.json` first — at minimum check:

| Parameter | Notes |
|-----------|-------|
| `namePrefix` | Used to derive all resource names (lowercase, ≤12 chars). |
| `location` | Region for the Container Apps environment, storage, and Log Analytics. |
| `openAiLocation` | Region for the Azure OpenAI account. Must support `text-embedding-3-small`. |
| `embeddingModelName` / `embeddingDimensions` | Keep these in sync — see the tradeoff above. |
| `embeddingCapacity` | Provisioned throughput in 1K-TPM units for the embedding deployment. |

The deployment provisions: a Log Analytics workspace, the Container Apps
environment, a storage account with two Azure Files shares, an Azure OpenAI
account + embedding model deployment, the internal `qdrant` container app, and
the public `conduit` container app — fully wired together via environment
variables (see [Configuration reference](configuration.md) for what each one
does).

When it finishes, grab the app URL:

```bash
az deployment group show \
  --resource-group rg-conduit --name main \
  --query properties.outputs.conduitUrl.value -o tsv
```

## 2. Sign in and retrieve your MCP key

- **Browser**: open the Conduit URL. If you passed `conduitOwnerEmail` /
  `conduitOwnerPassword` at deploy time, sign in with those; otherwise the
  first visit shows the one-time `/setup` page where you create the owner
  account (it is stored bcrypt-hashed in `owner.json` on the `conduit-data`
  share and the setup page disables itself afterwards).
- **MCP clients** authenticate with the `CONDUIT_API_KEY` secret generated
  during deployment. Retrieve it:

```bash
az containerapp secret list \
  --name <namePrefix>-conduit --resource-group rg-conduit \
  --show-values --query "[?name=='conduit-api-key'].value" -o tsv
```

Then send it as a bearer token, e.g.:

```json
{
  "mcpServers": {
    "conduit": {
      "type": "http",
      "url": "https://<conduit-fqdn>/mcp",
      "headers": { "Authorization": "Bearer <conduit-api-key>" }
    }
  }
}
```

To rotate the key later:

```bash
az containerapp secret set \
  --name <namePrefix>-conduit --resource-group rg-conduit \
  --secrets conduit-api-key=<new-key>

az containerapp revision restart --name <namePrefix>-conduit --resource-group rg-conduit
```

## 3. First run

Open the Conduit URL (you'll be prompted for the access key above). The
**Settings** page should already show:

- **Embedding provider**: Azure OpenAI, with the endpoint and deployment name
  filled in from environment variables (the API key field stays blank — it's
  supplied via the `AZURE_OPENAI_API_KEY` secret, not the credential library).
- **Vector store**: the internal Qdrant FQDN, HTTPS enabled, with an API key.

These connection settings come from environment variables baked into the
Container App by Bicep and take precedence on every restart — changing them
in the UI only affects `/data/config.json` (which the embedding/Qdrant env
vars will continue to override). Other settings (chunking, preprocessing) do
persist normally via `/data/config.json` on the Azure Files share.

Use the **Verify** buttons on both cards to confirm Conduit can reach Qdrant
and Azure OpenAI.

## 4. Add source credentials

Azure DevOps PATs and other source credentials are managed entirely on the
`/credentials` page and stored Fernet-encrypted in
`/data/credentials.enc.json` on the `conduit-data` share — they're not part of
the Bicep deployment. Add them there, then create your sources as usual.

To keep the encryption key for `credentials.enc.json` stable across container
recreation (recommended), generate a key and add it as an extra secret/env var
on the `conduit` container app:

```bash
python -c "from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())"

az containerapp secret set \
  --name <namePrefix>-conduit --resource-group rg-conduit \
  --secrets conduit-secret-key=<generated-key>

az containerapp update \
  --name <namePrefix>-conduit --resource-group rg-conduit \
  --set-env-vars CONDUIT_SECRET_KEY=secretref:conduit-secret-key
```

Without this, a key is auto-generated and stored as `/data/.secret_key` on
first run — fine as long as the `conduit-data` share isn't deleted.

## Redeploying

`infra/main.bicep` is idempotent — re-running `az deployment group create`
with the same parameters updates the existing resources in place (new image
tags, scaling changes, etc.).

`qdrantApiKey` and `conduitApiKey` both default to freshly generated values
(`newGuid()`) on every deployment that doesn't pass them explicitly. A
redeploy that rotates `conduitApiKey` requires re-fetching it (step 2) and
updating any saved MCP client config. Pass both explicitly (e.g. from a
pipeline secret) if you'd rather pin them across redeploys.

## Known limitations / tradeoffs

- **Storage performance**: both Azure Files shares are Standard (SMB) tier.
  This is fine for portfolio/small-team scale; for larger Qdrant collections,
  consider Premium Files or switching the volume to `NfsAzureFile`.
- **Single replica**: both apps run `minReplicas: 1, maxReplicas: 1`. Qdrant's
  local-disk design and Conduit's in-process sync/credential state aren't
  designed for multiple concurrent replicas.
- **Local model support is unaffected**: setting `EMBEDDING_PROVIDER=openai-compatible`
  (the default when unset) and pointing `EMBEDDING_BASE_URL` at a reachable
  Ollama instance still works if you'd rather not use Azure OpenAI.
