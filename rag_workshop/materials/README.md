# Workshop Materials

This folder contains resources for **workshop organizers** to help participants prepare before workshop day.

## Contents

| File / Folder | Purpose |
|------|---------|
| [pre-workshop-email.md](pre-workshop-email.md) | Ready-to-send email template -- copy into Outlook/Gmail, fill in the `[PLACEHOLDER]` values, and send to participants ~1 week before the workshop |
| [docker-alternatives.md](docker-alternatives.md) | Free alternatives to Docker Desktop for companies that require a licence |
| [slides/](slides/) | Slidev presentation — project the slides during the workshop (`cd slides && npm install && npm run dev`) |

## Why pre-setup matters

The workshop needs Docker (for Qdrant) and an **OpenAI API key** (for embeddings + chat). Having participants install prerequisites and pull the Qdrant image **before** the workshop avoids a bandwidth bottleneck on the day.

| Pre-download | Size |
|-------------|------|
| Docker image (`qdrant/qdrant:v1.16.3`) | ~150 MB |

## Running the presentation

The workshop includes a **Slidev presentation** (~80 slides) that walks through each module with code examples, architecture diagrams, and exercises.

### Start the presentation

```bash
cd materials/slides
npm install        # first time only
npm run dev        # or: ./node_modules/.bin/slidev slides.md --open
```

The slides will open at **http://localhost:3030** in presenter mode.

> **Offline option**: A PDF export (`Qdrant.Demo.workshop.slides.pdf`) is available in the `materials/slides/` folder if you need to present without running Slidev.

### Presentation tips

- **Module structure**: Each module has concept slides followed by code walkthrough slides
- **Live coding**: The slides complement hands-on coding — show the slide, then code along with participants
- **Exercises**: Each module ends with "Try It" slides — give participants 5-10 minutes to experiment
- **Navigation**: Use arrow keys or click; press `O` for slide overview; press `F` for fullscreen
- **Dual screen**: Project slides on main screen, keep terminal/IDE on laptop screen

### Slidev shortcuts

| Key | Action |
|-----|--------|
| `→` / `←` | Next/previous slide |
| `Space` | Next slide |
| `O` | Overview mode (grid view) |
| `F` | Fullscreen |
| `D` | Dark/light mode |
| `Esc` | Exit modes |

## Participant setup (include in your pre-workshop email)

### 1. Install prerequisites

| Tool | Link |
|------|------|
| Docker Desktop | [docker.com/products/docker-desktop](https://www.docker.com/products/docker-desktop/) |
| .NET 10 SDK | [dotnet.microsoft.com/download/dotnet/10.0](https://dotnet.microsoft.com/download/dotnet/10.0) |
| Git | [git-scm.com](https://git-scm.com/) |

### 2. Pull the Qdrant Docker image

```bash
docker pull qdrant/qdrant:v1.16.3
```

### 3. Set the OpenAI API key

Each participant needs an OpenAI API key. Set it as an environment variable before running any module:

```powershell
# PowerShell
$env:OPENAI_API_KEY = "sk-..."
```

```bash
# bash / zsh
export OPENAI_API_KEY="sk-..."
```

> **Workshop organizer tip:** You can create a single project-scoped API key at [platform.openai.com/api-keys](https://platform.openai.com/api-keys) with a spending limit and share it with participants. Expected cost is ~$0.50 per participant for the full workshop.

### 4. Verify

```bash
docker images qdrant/qdrant --format "table {{.Repository}}\t{{.Tag}}"   # should show v1.16.3
dotnet --version                     # should show 10.0.x
echo $OPENAI_API_KEY                 # should show sk-...
```

## Corporate network notes

This setup is **corporate-network friendly** by design:

- **Docker** pulls `qdrant/qdrant` from **Docker Hub** -- works through corporate proxies that do SSL inspection
- The .NET API calls the **OpenAI API** via HTTPS -- .NET uses the **Windows certificate store**, so corporate CA certificates are trusted automatically
- No local AI model downloads needed -- no blocked registries
- The API runs via `dotnet run` natively -- no containers with SSL issues
