from __future__ import annotations

import io
import json
import uuid
from datetime import datetime, timezone
from typing import Optional

import pypdf
from fastapi import APIRouter, BackgroundTasks, Form, Request, UploadFile
from fastapi.responses import JSONResponse, RedirectResponse, Response
from qdrant_client.models import FieldCondition, Filter, MatchValue

from app import container
from app.templates_cfg import templates
from app.models import ConfigKeys, PayloadKeys, SourceDefinition, SourceTypes
from app.sources.factory import SOURCE_TYPE_META, collection_for
from app.store.source_config import _normalise_keys

router = APIRouter()


def _ctx(request: Request, **kwargs) -> dict:
    """Base context injected into every template."""
    return {
        "qdrant_ready": container.health.is_ready,
        "qdrant_error": container.health.error,
        **kwargs,
    }


# ── Index ──────────────────────────────────────────────────────────────────────

@router.get("/")
async def index(request: Request):
    sources = await container.config_store.get_all()
    return templates.TemplateResponse(request, "index.html", _ctx(request, sources=sources))


@router.post("/sync/{source_id}")
async def sync_source(source_id: str, background_tasks: BackgroundTasks):
    source = await container.config_store.get_by_id(source_id)
    if source and source.sync_status != "syncing":
        background_tasks.add_task(container.sync_service.sync, source_id)
    return RedirectResponse("/", status_code=303)


@router.get("/status")
async def status():
    sources = await container.config_store.get_all()
    result = []
    for s in sources:
        p = container.progress_store.get(s.id)
        result.append({
            "id": s.id,
            "syncStatus": s.sync_status,
            "syncError": s.sync_error,
            "lastSyncedAt": s.last_synced_at.strftime("%Y-%m-%d %H:%M") if s.last_synced_at else None,
            "syncPhase": p.phase if p else None,
            "syncCurrent": p.current if p else None,
            "syncTotal": p.total if p else None,
            "syncMessage": p.message if p else None,
        })
    return JSONResponse(result)


@router.get("/export")
async def export_sources():
    data = container.config_store.export_stripped()
    content = json.dumps(data, indent=2, default=str)
    return Response(
        content=content,
        media_type="application/json",
        headers={"Content-Disposition": "attachment; filename=conduit-sources.json"},
    )


@router.post("/import")
async def import_sources(request: Request, file: UploadFile):
    try:
        raw = await file.read()
        items = json.loads(raw)
        existing = await container.config_store.get_all()
        existing_ids = {s.id for s in existing}

        imported = 0
        for item in items:
            item = _normalise_keys(item)
            s = SourceDefinition.model_validate(item)
            if s.id in existing_ids:
                s.id = str(uuid.uuid4())
            s.sync_status = "idle"
            s.sync_error = None
            await container.config_store.save(s)
            imported += 1

        sources = await container.config_store.get_all()
        return templates.TemplateResponse(request, "index.html", _ctx(
            request,
            sources=sources,
            import_message=f"Imported {imported} source(s). Fill in credentials before syncing.",
        ))
    except Exception as exc:
        sources = await container.config_store.get_all()
        return templates.TemplateResponse(request, "index.html", _ctx(
            request,
            sources=sources,
            import_error=f"Import failed: {exc}",
        ))


# ── Settings ───────────────────────────────────────────────────────────────────

@router.get("/settings")
async def settings_get(request: Request):
    from app.config import get_config, get_config_path
    cfg = get_config()
    return templates.TemplateResponse(request, "settings.html", _ctx(
        request,
        cfg=cfg,
        config_path=get_config_path(),
        status_message=None,
        was_dropped=False,
    ))


@router.post("/settings")
async def settings_post(
    request: Request,
    provider: str = Form(...),
    model: str = Form(...),
    api_key_env_var: str = Form(""),
    base_url: str = Form(""),
    dimensions: int = Form(1536),
    max_input_chars: int = Form(8000),
    qdrant_host: str = Form("localhost"),
    qdrant_port: int = Form(6333),
):
    from app.config import AppConfig, EmbeddingConfig, QdrantConfig, get_config, get_config_path, save_config
    from app.models import CollectionNames

    old_cfg = get_config()
    new_cfg = AppConfig(
        embedding=EmbeddingConfig(
            provider=provider,
            model=model,
            api_key_env_var=api_key_env_var,
            base_url=base_url,
            dimensions=dimensions,
            max_input_chars=max_input_chars,
        ),
        qdrant=QdrantConfig(host=qdrant_host, port=qdrant_port),
        chunking=old_cfg.chunking,
        sources_file_path=old_cfg.sources_file_path,
    )

    embedding_changed = (
        old_cfg.embedding.model != new_cfg.embedding.model
        or old_cfg.embedding.dimensions != new_cfg.embedding.dimensions
        or old_cfg.embedding.provider != new_cfg.embedding.provider
        or old_cfg.embedding.base_url != new_cfg.embedding.base_url
    )

    save_config(new_cfg)
    was_dropped = False

    if embedding_changed:
        was_dropped = True
        for col in CollectionNames.ALL:
            try:
                if await container.vector_store.collection_exists(col):
                    await container.vector_store.delete_collection(col)
            except Exception:
                pass
        await container.config_store.reset_all_sync_status("needs-reindex")

    return templates.TemplateResponse(request, "settings.html", _ctx(
        request,
        cfg=new_cfg,
        config_path=get_config_path(),
        status_message="saved",
        was_dropped=was_dropped,
    ))


# ── Sources: Create ────────────────────────────────────────────────────────────

@router.get("/sources/create")
async def sources_create_get(request: Request, type: str = ""):
    type_label = next((label for t, label, _ in SOURCE_TYPE_META if t == type), "")
    return templates.TemplateResponse(request, "sources/create.html", _ctx(
        request,
        selected_type=type,
        type_label=type_label,
        source_types=SOURCE_TYPE_META,
        source=SourceDefinition(type=type, name=""),
    ))


@router.post("/sources/create")
async def sources_create_post(request: Request, background_tasks: BackgroundTasks):
    form = await request.form()
    source = _build_source_from_form(form)

    if source.type == SourceTypes.MANUAL_DOCUMENT:
        file = form.get("ConfigFile")
        if file and hasattr(file, "filename") and file.filename:
            text = await _read_upload(file)
            if text:
                source.config[ConfigKeys.CONTENT] = text
                source.config[ConfigKeys.TITLE] = source.config.get(ConfigKeys.TITLE) or file.filename

    await container.config_store.save(source)
    background_tasks.add_task(container.sync_service.sync, source.id)
    return RedirectResponse("/", status_code=303)


# ── Sources: Edit ──────────────────────────────────────────────────────────────

@router.get("/sources/{source_id}/edit")
async def sources_edit_get(request: Request, source_id: str):
    source = await container.config_store.get_by_id(source_id)
    if not source:
        return RedirectResponse("/")
    type_label = next((label for t, label, _ in SOURCE_TYPE_META if t == source.type), source.type)
    return templates.TemplateResponse(request, "sources/edit.html", _ctx(
        request,
        source=source,
        type_label=type_label,
    ))


@router.post("/sources/{source_id}/edit")
async def sources_edit_post(request: Request, source_id: str, background_tasks: BackgroundTasks):
    existing = await container.config_store.get_by_id(source_id)
    if not existing:
        return RedirectResponse("/")
    form = await request.form()
    updated = _build_source_from_form(form)
    updated.id = source_id
    updated.last_synced_at = existing.last_synced_at
    updated.sync_status = "idle"
    updated.sync_error = None
    await container.config_store.save(updated)
    return RedirectResponse("/", status_code=303)


# ── Sources: Delete ────────────────────────────────────────────────────────────

@router.get("/sources/{source_id}/delete")
async def sources_delete_get(request: Request, source_id: str):
    source = await container.config_store.get_by_id(source_id)
    if not source:
        return RedirectResponse("/")
    return templates.TemplateResponse(request, "sources/delete.html", _ctx(
        request,
        source=source,
    ))


@router.post("/sources/{source_id}/delete")
async def sources_delete_post(source_id: str):
    source = await container.config_store.get_by_id(source_id)
    if source:
        collection = collection_for(source.type)
        try:
            filt = Filter(must=[
                FieldCondition(key=PayloadKeys.tag("source_name"), match=MatchValue(value=source.name))
            ])
            await container.vector_store.delete_by_filter(collection, filt)
        except Exception:
            pass  # Qdrant may be offline; config deletion proceeds regardless
    await container.config_store.delete(source_id)
    return RedirectResponse("/", status_code=303)


# ── Sources: Browse items ──────────────────────────────────────────────────────

@router.get("/sources/{source_id}/items")
async def sources_items_get(
    request: Request, source_id: str, offset: Optional[str] = None
):
    source = await container.config_store.get_by_id(source_id)
    if not source:
        return RedirectResponse("/")

    collection = collection_for(source.type)
    scroll_filter = Filter(must=[
        FieldCondition(key="tag_source_name", match=MatchValue(value=source.name))
    ])

    items = []
    next_offset = None
    try:
        points, next_offset = await container.vector_store.scroll(
            collection, scroll_filter=scroll_filter, limit=20, offset=offset
        )
        for p in points:
            payload = p.payload or {}
            indexed_ms = payload.get(PayloadKeys.INDEXED_AT_MS)
            indexed_at = (
                datetime.fromtimestamp(indexed_ms / 1000, tz=timezone.utc)
                if indexed_ms else None
            )
            items.append({
                "title": payload.get("prop_title", ""),
                "url": payload.get("prop_url", ""),
                "text": payload.get(PayloadKeys.TEXT, ""),
                "chunk_index": payload.get(PayloadKeys.CHUNK_INDEX),
                "total_chunks": payload.get(PayloadKeys.TOTAL_CHUNKS),
                "indexed_at": indexed_at.strftime("%Y-%m-%d %H:%M") if indexed_at else None,
            })
    except Exception:
        pass

    return templates.TemplateResponse(request, "sources/items.html", _ctx(
        request,
        source=source,
        items=items,
        next_offset=next_offset,
        has_prev=offset is not None,
    ))


# ── Experience ─────────────────────────────────────────────────────────────────

@router.get("/experience")
async def experience_list(request: Request, offset: Optional[str] = None):
    entries, next_offset = await container.memory_service.get_all_paginated(limit=20, offset=offset)
    total = await container.memory_service.count()
    return templates.TemplateResponse(request, "experience/list.html", _ctx(
        request,
        entries=entries,
        next_offset=next_offset,
        has_prev=offset is not None,
        total=total,
    ))


@router.get("/experience/create")
async def experience_create_get(request: Request):
    return templates.TemplateResponse(request, "experience/create.html", _ctx(request))


@router.post("/experience/add")
async def experience_add(
    content: str = Form(...),
    category: str = Form("general"),
    importance: int = Form(3),
):
    content = content.strip()
    if content:
        await container.memory_service.remember(content, category, importance)
    return RedirectResponse("/experience", status_code=303)


@router.post("/experience/{entry_id}/delete")
async def experience_delete(entry_id: str):
    await container.memory_service.delete(entry_id)
    return RedirectResponse("/experience", status_code=303)


@router.get("/experience/map")
async def experience_map(request: Request):
    total = await container.memory_service.count()
    return templates.TemplateResponse(request, "experience/map.html", _ctx(
        request,
        total=total,
    ))


@router.get("/api/experience/map-data")
async def experience_map_data():
    try:
        import numpy as np
        from sklearn.decomposition import PCA
    except ImportError:
        return JSONResponse({"error": "scikit-learn not installed. Run: uv pip install scikit-learn"}, status_code=500)

    try:
        entries = await container.memory_service.get_all_with_vectors()
    except Exception as exc:
        return JSONResponse({"error": f"Failed to load entries: {exc}", "points": []}, status_code=500)

    if len(entries) < 2:
        return JSONResponse({"points": [], "note": f"Need at least 2 entries (have {len(entries)})."})

    try:
        vectors = np.array([e["vector"] for e in entries], dtype=float)
        # n_components must be <= min(n_samples, n_features); with 2 samples max rank is 1
        n = min(2, len(entries), vectors.shape[1])
        coords = PCA(n_components=n).fit_transform(vectors)
    except Exception as exc:
        return JSONResponse({"error": f"PCA failed: {exc}", "points": []}, status_code=500)

    points = []
    for i, e in enumerate(entries):
        points.append({
            "id": e["id"],
            "x": float(coords[i, 0]),
            "y": float(coords[i, 1]) if n >= 2 else 0.0,
            "text": e["text"],
            "category": e["category"],
            "importance": e["importance"],
            "created_at": e["created_at"],
        })
    return JSONResponse({"points": points})


# ── Helpers ────────────────────────────────────────────────────────────────────

def _build_source_from_form(form) -> SourceDefinition:
    source_type = str(form.get("source_type") or form.get("Source.Type") or "")
    name = str(form.get("name") or form.get("Source.Name") or "")
    source_id = str(form.get("Source.Id") or uuid.uuid4())

    config: dict[str, str] = {}

    def _set(key: str, *form_keys: str, default: str = "") -> None:
        for fk in form_keys:
            val = form.get(fk)
            if val:
                config[key] = str(val)
                return
        if default:
            config[key] = default

    _set(ConfigKeys.BASE_URL, "ConfigBaseUrl")
    _set(ConfigKeys.AUTH_TYPE, "ConfigAuthType")
    _set(ConfigKeys.API_VERSION, "ConfigApiVersion")

    auth_type = config.get(ConfigKeys.AUTH_TYPE, "none")
    if auth_type == "pat":
        _set(ConfigKeys.PAT, "ConfigPat")
    elif auth_type == "bearer":
        _set(ConfigKeys.TOKEN, "ConfigToken")
    elif auth_type in ("ntlm", "negotiate"):
        _set(ConfigKeys.USERNAME, "ConfigUsername")
        _set(ConfigKeys.PASSWORD, "ConfigPassword")
        _set(ConfigKeys.DOMAIN, "ConfigDomain")
    elif auth_type == "apikey":
        _set(ConfigKeys.API_KEY_HEADER, "ConfigApiKeyHeader")
        _set(ConfigKeys.API_KEY_VALUE, "ConfigApiKeyValue")

    if source_type == SourceTypes.MANUAL_DOCUMENT:
        _set(ConfigKeys.CONTENT, "ConfigContent")
        _set(ConfigKeys.TITLE, "ConfigTitle")

    if source_type in (SourceTypes.ADO_WORK_ITEM_QUERY, SourceTypes.ADO_REQUIREMENTS, SourceTypes.ADO_TEST_CASE):
        _set(ConfigKeys.QUERY, "ConfigQuery")
        _set(ConfigKeys.FIELDS, "ConfigFields")

    if source_type == SourceTypes.ADO_CODE_REPO:
        _set(ConfigKeys.REPOSITORY, "ConfigRepository")
        _set(ConfigKeys.BRANCH, "ConfigBranch", default="main")
        _set(ConfigKeys.GLOB_PATTERNS, "ConfigGlobPatterns", default="**/*.cs")

    if source_type == SourceTypes.ADO_PIPELINE_BUILD:
        _set(ConfigKeys.PIPELINE_ID, "ConfigPipelineId")
        _set(ConfigKeys.LAST_N_BUILDS, "ConfigLastNBuilds", default="5")

    if source_type == SourceTypes.ADO_WIKI:
        _set(ConfigKeys.WIKI_NAME, "ConfigWikiName")
        _set(ConfigKeys.PATH_FILTER, "ConfigPathFilter")

    if source_type == SourceTypes.HTTP_PAGE:
        _set(ConfigKeys.URL, "ConfigUrl")
        _set(ConfigKeys.TITLE, "ConfigTitle")
        _set(ConfigKeys.CONTENT_TYPE, "ConfigContentType", default="auto")

    return SourceDefinition(id=source_id, type=source_type, name=name, config=config)


async def _read_upload(file: UploadFile) -> str:
    content = await file.read()
    if file.filename and file.filename.lower().endswith(".pdf"):
        reader = pypdf.PdfReader(io.BytesIO(content))
        return "\n\n".join(page.extract_text() or "" for page in reader.pages)
    return content.decode("utf-8", errors="replace")
