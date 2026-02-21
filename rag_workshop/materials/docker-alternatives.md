# Docker Desktop Alternatives

Docker Desktop's free **Personal** tier is limited to: personal use, educational use, non-commercial open-source projects, and **small businesses** (fewer than 250 employees **and** less than $10,000,000 in annual revenue). If your organisation exceeds either threshold, a paid subscription is required ([pricing details](https://www.docker.com/pricing/)).

If that applies to you, here are free alternatives that work with this workshop.

---

## Option 1 — Rancher Desktop ⭐ Recommended

[Rancher Desktop](https://rancherdesktop.io/) is a free, open-source desktop application that provides container management and Kubernetes on Windows, macOS, and Linux. It supports both the `dockerd` (moby) and `containerd` container runtimes.

**Why it's recommended:** drop-in Docker replacement — the `docker` and `docker compose` CLI commands work unchanged, so every workshop instruction works as-is.

### Setup

1. Download and install from [rancherdesktop.io](https://rancherdesktop.io/)
2. During setup, choose **dockerd (moby)** as the container runtime
3. Verify:

```bash
docker --version
docker compose version
```

4. Continue with the workshop — no other changes needed

---

## Option 2 — Podman + Podman Desktop

[Podman](https://podman.io/) is a daemonless, open-source container engine developed by Red Hat. [Podman Desktop](https://podman-desktop.io/) adds a GUI experience similar to Docker Desktop.

### Setup

1. Install Podman Desktop from [podman-desktop.io](https://podman-desktop.io/)
2. Initialise a Podman machine:

```bash
podman machine init
podman machine start
```

3. **Enable Docker compatibility** — replace `docker` commands with `podman`:

```bash
# Option A: shell alias (temporary)
Set-Alias -Name docker -Value podman      # PowerShell
alias docker=podman                        # bash/zsh

# Option B: use Podman Desktop's Docker compatibility mode
#   (Settings → Experimental → Docker Compatibility)
```

4. For `docker compose`, install **podman-compose**:

```bash
pip install podman-compose
```

Or use Podman Desktop's built-in Compose support.

### Workshop adjustments

Replace `docker compose` with `podman-compose` (or `podman compose` if using the Compose plugin):

```bash
podman-compose up -d    # instead of: docker compose up -d
podman-compose down     # instead of: docker compose down
```

---

## Option 3 — Docker Engine CE on WSL 2 (Windows only)

Docker Engine Community Edition is **free for all uses** — the paid licence applies only to Docker Desktop. You can run the engine directly inside WSL 2.

### Setup

1. Ensure WSL 2 is installed:

```powershell
wsl --install
```

2. Inside your WSL distribution (e.g. Ubuntu):

```bash
# Install Docker Engine (official docs: https://docs.docker.com/engine/install/ubuntu/)
sudo apt-get update
sudo apt-get install -y ca-certificates curl gnupg
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin

# Start Docker
sudo service docker start

# (Optional) Allow running without sudo
sudo usermod -aG docker $USER
```

3. All workshop commands work unchanged inside the WSL terminal.

### Workshop adjustments

Run all commands from a **WSL terminal** (not PowerShell). The .NET SDK also needs to be installed inside WSL, or use `dotnet` from Windows with Qdrant running in WSL — both approaches work.

---

## Option 4 — Colima (macOS / Linux)

[Colima](https://github.com/abiosoft/colima) is a lightweight container runtime for macOS and Linux. It provides Docker-compatible CLI support with minimal overhead.

### Setup

```bash
# macOS
brew install colima docker docker-compose

# Start Colima (uses Lima VM under the hood)
colima start

# Verify
docker --version
docker compose version
```

All workshop commands work unchanged.

---

## Option 5 — Qdrant Binary (no containers at all)

If containers are not an option, you can run Qdrant as a standalone binary.

### Setup

1. Download the latest release from [github.com/qdrant/qdrant/releases](https://github.com/qdrant/qdrant/releases)
2. Extract and run:

```bash
./qdrant                # Linux / macOS
qdrant.exe              # Windows
```

Qdrant will start on the default ports: **6333** (REST) and **6334** (gRPC).

### Workshop adjustments

- Skip `docker compose up -d` and `docker compose down` — start/stop the binary manually instead
- Everything else (API endpoints, ports, Qdrant Dashboard) works the same

---

## Option 6 — Qdrant Cloud (no local install)

[Qdrant Cloud](https://cloud.qdrant.io/) offers a **free tier** (1 GB cluster) that's more than enough for this workshop.

### Setup

1. Sign up at [cloud.qdrant.io](https://cloud.qdrant.io/)
2. Create a free cluster — note the **URL** and **API key**
3. Update each module's `Program.cs` to point at the cloud instance:

```csharp
// Replace local Qdrant
var qdrantClient = new QdrantClient(
    host: "your-cluster-id.cloud.qdrant.io",
    https: true,
    apiKey: "your-api-key");
```

### Workshop adjustments

- Skip `docker compose up -d` entirely
- Change the Qdrant client configuration in `Program.cs` (host, HTTPS, API key)
- The Qdrant Dashboard is available in the Qdrant Cloud console instead of `localhost:6333/dashboard`

---

## Comparison

| Option | OS | Effort | CLI compatible | Free |
|--------|----|--------|---------------|------|
| **Rancher Desktop** | Win / Mac / Linux | ⭐ Low | ✅ Yes | ✅ Yes |
| **Podman** | Win / Mac / Linux | Medium | ⚠️ Alias needed | ✅ Yes |
| **Docker Engine CE (WSL 2)** | Windows | Medium | ✅ Yes | ✅ Yes |
| **Colima** | Mac / Linux | Low | ✅ Yes | ✅ Yes |
| **Qdrant Binary** | Win / Mac / Linux | Low | N/A (no Docker) | ✅ Yes |
| **Qdrant Cloud** | Any | Low | N/A (no Docker) | ✅ Free tier |

---

## Which should I pick?

- **Easiest path (Windows / macOS):** Rancher Desktop — install and go, no command changes
- **Already using Podman?** Podman Desktop + alias works well
- **Linux-first workflow:** Docker Engine CE inside WSL 2
- **macOS minimalist:** Colima
- **No containers allowed at all:** Qdrant binary or Qdrant Cloud free tier
