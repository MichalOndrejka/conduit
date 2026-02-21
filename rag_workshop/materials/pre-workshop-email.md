# Pre-Workshop Email Template

> Copy the text below into your email client. Replace the `[PLACEHOLDER]` values with your details.

---

**Subject:** You're Invited -- RAG Workshop on [DATE]

Hi everyone,

You're invited to a hands-on **RAG Workshop** on **[DATE]** at **[TIME]** in **[LOCATION / Teams link]**.

## What is this about?

**Retrieval-Augmented Generation (RAG)** is one of the most practical ways to combine Large Language Models with your own data. Instead of hoping the AI "knows" the answer, you feed it the right documents first — so every response is grounded in facts, not guesswork.

In this workshop, we'll build a complete RAG solution from scratch using **.NET 10, Qdrant (a vector database), and OpenAI**. The workshop combines guided slides with hands-on coding — you'll build each module step by step.

**Workshop repository:** 

[https://github.com/PeterMilovcik/Qdrant.Demo](https://github.com/PeterMilovcik/Qdrant.Demo)

> Bookmark it now so you have it ready on workshop day.

## What you'll walk away with

By the end of the ~3-hour session, you'll have built a working API that can:

- Turn documents into **vector embeddings** and store them in a database
- Find documents by **meaning** (not keywords) using cosine similarity
- Filter results by **metadata tags**
- Answer questions using **OpenAI**, grounded in your own documents
- Handle long documents by **chunking** them intelligently
- Process **batch uploads** efficiently
- Serve a **chat UI** where you can interact with everything visually

Every module is self-contained and fully working — you'll run and test each one as you go.

## Who is this for?

Anyone comfortable with **C# / .NET** who's curious about AI, embeddings, vector databases, or RAG. No prior AI or ML experience needed — we start from zero and build up.

## What to prepare (10 minutes)

To keep workshop day smooth, please install the following ahead of time:

| # | Tool | Link |
|---|------|------|
| 1 | **Docker Desktop** | [docker.com/products/docker-desktop](https://www.docker.com/products/docker-desktop/) — if your company requires a licence alternative, see [Docker Alternatives](docker-alternatives.md) |
| 2 | **.NET 10 SDK** | [dotnet.microsoft.com/download/dotnet/10.0](https://dotnet.microsoft.com/download/dotnet/10.0) |
| 3 | **Git** | [git-scm.com](https://git-scm.com/) |
| 4 | **VS Code** *(recommended)* | [code.visualstudio.com](https://code.visualstudio.com/) — with the **C# Dev Kit** extension |

Then clone the repo and pull the Qdrant Docker image (~150 MB) so we don't all download it at the same time:

```bash
git clone https://github.com/PeterMilovcik/Qdrant.Demo.git
cd Qdrant.Demo
docker pull qdrant/qdrant:v1.16.3
```

Quick sanity check:

```bash
docker --version        # 27.x or later
dotnet --version        # 10.0.101 or later
```

If both work — you're all set!

> **OpenAI API key** will be provided at the start of the session. You don't need your own.

## Questions?

Reply to this email or ping me on Teams. Happy to help with setup if anything doesn't work.

Looking forward to building this with you on **[DATE]**!

[YOUR NAME]

---

> *This template lives in the [materials/](https://github.com/PeterMilovcik/Qdrant.Demo/tree/main/materials) folder of the workshop repository.*
