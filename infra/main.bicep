// =====================================================================================
// Conduit — Azure Container Apps deployment
//
// Provisions:
//   - Log Analytics workspace + Container Apps Environment
//   - Storage account with two Azure Files shares (Qdrant data, Conduit data)
//   - Azure OpenAI account + an embedding model deployment
//   - Qdrant container app — internal ingress only, persistent volume, minReplicas 1
//   - Conduit container app — external ingress, wired to Qdrant + Azure OpenAI
//
// Deploy:
//   az deployment group create \
//     --resource-group <resource-group> \
//     --template-file infra/main.bicep \
//     --parameters namePrefix=conduit openAiLocation=eastus
//
// See docs/deployment-azure.md for the full walkthrough and post-deploy steps.
// =====================================================================================

@description('Short name used as a prefix for all resource names. Lowercase letters and numbers only.')
@minLength(3)
@maxLength(12)
param namePrefix string = 'conduit'

@description('Azure region for the Container Apps environment, storage account, and Log Analytics workspace.')
param location string = resourceGroup().location

@description('Azure region for the Azure OpenAI account. Must be a region where the chosen embedding model is available (e.g. eastus, swedencentral, westus).')
param openAiLocation string = location

@description('Container image for Conduit (Go). Build and push with: docker build -f Dockerfile.golang -t michalondrejka/conduit:go-latest . && docker push michalondrejka/conduit:go-latest')
param conduitImage string = 'michalondrejka/conduit:go-latest'

@description('Qdrant container image.')
param qdrantImage string = 'qdrant/qdrant:v1.13.6'

@description('Azure OpenAI embedding model name.')
param embeddingModelName string = 'text-embedding-3-small'

@description('Azure OpenAI embedding model version.')
param embeddingModelVersion string = '1'

@description('Vector dimensions for the embedding model. text-embedding-3-small = 1536, text-embedding-3-large = 3072. Must match embeddingModelName.')
param embeddingDimensions int = 1536

@description('Provisioned throughput for the embedding deployment, in units of 1,000 tokens-per-minute.')
param embeddingCapacity int = 10

@description('Size of the Qdrant Azure Files share, in GiB.')
param qdrantShareSizeGiB int = 50

@description('Size of the Conduit data Azure Files share, in GiB.')
param conduitShareSizeGiB int = 5

@secure()
@description('Shared key used to authenticate Conduit -> Qdrant traffic (sets QDRANT__SERVICE__API_KEY). Defaults to a freshly generated value on every deployment that omits this parameter.')
param qdrantApiKey string = newGuid()

@secure()
@description('Bearer key for headless MCP/API clients (sets CONDUIT_API_KEY). Defaults to a freshly generated value on every deployment that omits this parameter — retrieve it after deploy with az containerapp secret list.')
param conduitApiKey string = newGuid()

@description('Owner account email for the web UI login (n8n-style). Leave empty to use the first-run /setup page instead.')
param conduitOwnerEmail string = ''

@secure()
@description('Owner account password. Bcrypt-hashed in memory at startup; never stored in plaintext. Leave empty (with conduitOwnerEmail empty) to use the first-run /setup page.')
param conduitOwnerPassword string = ''

var namePrefixLower = toLower(namePrefix)
var storageAccountName = take('${namePrefixLower}st${uniqueString(resourceGroup().id)}', 24)
var openAiName = take('${namePrefixLower}-aoai-${uniqueString(resourceGroup().id)}', 64)
var qdrantShareName = 'qdrant-storage'
var conduitShareName = 'conduit-data'

// ── Observability ────────────────────────────────────────────────────────────────

resource logAnalytics 'Microsoft.OperationalInsights/workspaces@2022-10-01' = {
  name: '${namePrefixLower}-logs'
  location: location
  properties: {
    sku: { name: 'PerGB2018' }
    retentionInDays: 30
  }
}

// ── Container Apps Environment ───────────────────────────────────────────────────

resource environment 'Microsoft.App/managedEnvironments@2023-05-01' = {
  name: '${namePrefixLower}-env'
  location: location
  properties: {
    appLogsConfiguration: {
      destination: 'log-analytics'
      logAnalyticsConfiguration: {
        customerId: logAnalytics.properties.customerId
        sharedKey: logAnalytics.listKeys().primarySharedKey
      }
    }
  }
}

// ── Storage (Azure Files for Qdrant + Conduit data) ──────────────────────────────
// ACA's local container disk is ephemeral, so both the Qdrant collection data and
// Conduit's /data (sources, sync state, encrypted credential store) live on
// Azure Files shares mounted into each container app.

resource storage 'Microsoft.Storage/storageAccounts@2023-01-01' = {
  name: storageAccountName
  location: location
  kind: 'StorageV2'
  sku: { name: 'Standard_LRS' }
  properties: {
    largeFileSharesState: 'Enabled'
    minimumTlsVersion: 'TLS1_2'
  }
}

resource fileServices 'Microsoft.Storage/storageAccounts/fileServices@2023-01-01' = {
  parent: storage
  name: 'default'
}

resource qdrantShare 'Microsoft.Storage/storageAccounts/fileServices/shares@2023-01-01' = {
  parent: fileServices
  name: qdrantShareName
  properties: {
    shareQuota: qdrantShareSizeGiB
    enabledProtocols: 'SMB'
  }
}

resource conduitShare 'Microsoft.Storage/storageAccounts/fileServices/shares@2023-01-01' = {
  parent: fileServices
  name: conduitShareName
  properties: {
    shareQuota: conduitShareSizeGiB
    enabledProtocols: 'SMB'
  }
}

resource qdrantStorageDef 'Microsoft.App/managedEnvironments/storages@2023-05-01' = {
  parent: environment
  name: qdrantShareName
  properties: {
    azureFile: {
      accountName: storage.name
      accountKey: storage.listKeys().keys[0].value
      shareName: qdrantShareName
      accessMode: 'ReadWrite'
    }
  }
  dependsOn: [
    qdrantShare
  ]
}

resource conduitStorageDef 'Microsoft.App/managedEnvironments/storages@2023-05-01' = {
  parent: environment
  name: conduitShareName
  properties: {
    azureFile: {
      accountName: storage.name
      accountKey: storage.listKeys().keys[0].value
      shareName: conduitShareName
      accessMode: 'ReadWrite'
    }
  }
  dependsOn: [
    conduitShare
  ]
}

// ── Azure OpenAI (embeddings) ────────────────────────────────────────────────────

resource openAi 'Microsoft.CognitiveServices/accounts@2023-05-01' = {
  name: openAiName
  location: openAiLocation
  kind: 'OpenAI'
  sku: { name: 'S0' }
  properties: {
    customSubDomainName: openAiName
    publicNetworkAccess: 'Enabled'
  }
}

resource embeddingDeployment 'Microsoft.CognitiveServices/accounts/deployments@2023-05-01' = {
  parent: openAi
  name: embeddingModelName
  sku: {
    name: 'Standard'
    capacity: embeddingCapacity
  }
  properties: {
    model: {
      format: 'OpenAI'
      name: embeddingModelName
      version: embeddingModelVersion
    }
  }
}

// ── Qdrant — internal-only, persistent, single replica ───────────────────────────
// No public ingress: only reachable from other apps in this Container Apps
// environment via its internal FQDN. minReplicas=1 keeps it warm so it never
// cold-starts and loses its in-memory index between requests.

resource qdrant 'Microsoft.App/containerApps@2023-05-01' = {
  name: '${namePrefixLower}-qdrant'
  location: location
  properties: {
    managedEnvironmentId: environment.id
    configuration: {
      activeRevisionsMode: 'Single'
      ingress: {
        external: false
        targetPort: 6333
        transport: 'auto'
        allowInsecure: false
      }
      secrets: [
        { name: 'qdrant-api-key', value: qdrantApiKey }
      ]
    }
    template: {
      containers: [
        {
          name: 'qdrant'
          image: qdrantImage
          resources: {
            cpu: json('1.0')
            memory: '2Gi'
          }
          env: [
            { name: 'QDRANT__SERVICE__API_KEY', secretRef: 'qdrant-api-key' }
          ]
          volumeMounts: [
            { volumeName: 'qdrant-storage', mountPath: '/qdrant/storage' }
          ]
        }
      ]
      volumes: [
        {
          name: 'qdrant-storage'
          storageType: 'AzureFile'
          storageName: qdrantStorageDef.name
        }
      ]
      scale: {
        minReplicas: 1
        maxReplicas: 1
      }
    }
  }
}

// ── Conduit — public RAG + MCP server ────────────────────────────────────────────

resource conduit 'Microsoft.App/containerApps@2023-05-01' = {
  name: '${namePrefixLower}-conduit'
  location: location
  properties: {
    managedEnvironmentId: environment.id
    configuration: {
      activeRevisionsMode: 'Single'
      ingress: {
        external: true
        targetPort: 8000
        transport: 'auto'
      }
      secrets: concat([
        { name: 'qdrant-api-key', value: qdrantApiKey }
        { name: 'azure-openai-api-key', value: openAi.listKeys().key1 }
        { name: 'conduit-api-key', value: conduitApiKey }
      ], empty(conduitOwnerPassword) ? [] : [
        { name: 'conduit-owner-password', value: conduitOwnerPassword }
      ])
    }
    template: {
      containers: [
        {
          name: 'conduit'
          image: conduitImage
          resources: {
            cpu: json('1.0')
            memory: '2Gi'
          }
          env: concat([
            { name: 'CONDUIT_DATA_DIR', value: '/data' }
            { name: 'CONDUIT_CONFIG', value: '/data/config.json' }
            { name: 'QDRANT_HOST', value: qdrant.properties.configuration.ingress.fqdn }
            { name: 'QDRANT_PORT', value: '443' }
            { name: 'QDRANT_HTTPS', value: 'true' }
            { name: 'QDRANT_API_KEY', secretRef: 'qdrant-api-key' }
            { name: 'EMBEDDING_PROVIDER', value: 'azure-openai' }
            { name: 'AZURE_OPENAI_ENDPOINT', value: openAi.properties.endpoint }
            { name: 'AZURE_OPENAI_DEPLOYMENT', value: embeddingModelName }
            { name: 'AZURE_OPENAI_API_VERSION', value: '2024-02-01' }
            { name: 'AZURE_OPENAI_API_KEY', secretRef: 'azure-openai-api-key' }
            { name: 'EMBEDDING_DIMENSIONS', value: string(embeddingDimensions) }
            { name: 'CONDUIT_API_KEY', secretRef: 'conduit-api-key' }
          ], empty(conduitOwnerEmail) ? [] : [
            { name: 'CONDUIT_OWNER_EMAIL', value: conduitOwnerEmail }
          ], empty(conduitOwnerPassword) ? [] : [
            { name: 'CONDUIT_OWNER_PASSWORD', secretRef: 'conduit-owner-password' }
          ])
          volumeMounts: [
            { volumeName: 'conduit-data', mountPath: '/data' }
          ]
        }
      ]
      volumes: [
        {
          name: 'conduit-data'
          storageType: 'AzureFile'
          storageName: conduitStorageDef.name
        }
      ]
      scale: {
        minReplicas: 1
        maxReplicas: 1
      }
    }
  }
  dependsOn: [
    embeddingDeployment
  ]
}

output conduitUrl string = 'https://${conduit.properties.configuration.ingress.fqdn}'
output qdrantInternalFqdn string = qdrant.properties.configuration.ingress.fqdn
output azureOpenAiEndpoint string = openAi.properties.endpoint
output azureOpenAiAccountName string = openAi.name
