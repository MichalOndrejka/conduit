# Install with Podman

Runs Conduit, Qdrant and Ollama from a clone (Podman can't load compose files
from a Git URL).

## Prerequisites

- [Podman](https://podman.io/docs/installation) with a compose provider
  ([Podman Desktop](https://podman-desktop.io) installs one)
- [Git](https://git-scm.com/downloads)
- 8 GB memory, 10 GB disk
- Windows: [WSL 2](https://learn.microsoft.com/windows/wsl/install) (`wsl --install`)

Windows and macOS only, once:

```
podman machine init --memory 8192
podman machine start
```

## Install

```
git clone https://github.com/MichalOndrejka/conduit.git
cd conduit
podman compose up
```

Wait for `Setup done` (the first run downloads the models), then open
http://localhost:8000.

## GPU (optional)

Without a GPU, models run on CPU. macOS, Intel and AMD on Windows always do
(AMD on Windows: use the [standalone container](standalone.md)).

**NVIDIA, Windows:** install a current [NVIDIA driver](https://www.nvidia.com/drivers), then:

```
wsl --update
podman machine ssh
```

Inside the machine:

```bash
curl -s -L https://nvidia.github.io/libnvidia-container/stable/rpm/nvidia-container-toolkit.repo | sudo tee /etc/yum.repos.d/nvidia-container-toolkit.repo
sudo yum install -y nvidia-container-toolkit
sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml
exit
```

**NVIDIA, Linux:** install the driver and the
[NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html), then:

```
sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml
```

Check the GPU is visible:

```
podman run --rm --device nvidia.com/gpu=all ubuntu nvidia-smi
```

**AMD, Linux:** install the ROCm driver ([supported cards](https://docs.ollama.com/gpu)).
On SELinux hosts (Fedora, RHEL):

```
sudo setsebool container_use_devices=1
```

Enable the GPU in the clone, once (replace `nvidia` with `amd` for AMD):

PowerShell:

```powershell
Add-Content .env "COMPOSE_PATH_SEPARATOR=:", "COMPOSE_FILE=docker-compose.yml:docker-compose.nvidia.yml"
```

bash:

```bash
printf 'COMPOSE_PATH_SEPARATOR=:\nCOMPOSE_FILE=docker-compose.yml:docker-compose.nvidia.yml\n' >> .env
```

Then `podman compose up`. Setup fails unless the models run fully on the GPU.

## Run in the background

Press Ctrl+C, then:

```
podman compose up -d
```

## Stop

```
podman compose down
```

## Update

```
git pull
podman compose pull
podman compose up -d
```

Proxy, models, data: [Customization](../customization.md).
