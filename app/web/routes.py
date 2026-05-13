from __future__ import annotations

import asyncio
import json
import uuid
from datetime import datetime, timezone
from typing import Optional

from fastapi import APIRouter, BackgroundTasks, Form, Request, UploadFile
from fastapi.responses import JSONResponse, RedirectResponse, Response
from qdrant_client.models import FieldCondition, Filter, MatchValue

from app import container
from app.templates_cfg import templates
from app.models import ConfigKeys, PayloadKeys, SourceDefinition, SourceTypes
from app.sources.factory import PLATFORMS, PROVIDERS, SOURCE_TYPE_META, collection_for
from app.store.source_config import _normalise_keys

router = APIRouter()


async def _extract_pdf_text(file) -> str:
    """Extract plain text from an uploaded PDF using pypdf."""
    import io
    from pypdf import PdfReader
    data = await file.read()
    reader = PdfReader(io.BytesIO(data))
    pages = []
    for page in reader.pages:
        text = page.extract_text() or ""
        if text.strip():
            pages.append(text)
    return "\n\n".join(pages)


async def _extract_file_text(file) -> str:
    """Extract plain text from an uploaded file (pdf, txt, md)."""
    filename = getattr(file, "filename", "") or ""
    ext = filename.rsplit(".", 1)[-1].lower() if "." in filename else ""
    if ext == "pdf":
        return await _extract_pdf_text(file)
    data = await file.read()
    return data.decode("utf-8", errors="replace")


def _ctx(request: Request, **kwargs) -> dict:
    """Base context injected into every template."""
    from app.config import get_config
    cfg = get_config()
    return {
        "qdrant_ready": container.health.is_ready,
        "qdrant_pending": container.health.pending,
        "qdrant_error": container.health.error,
        "qdrant_retry": container.health.retry_attempt,
        "qdrant_max_retries": container.health.max_retries,
        "embedding_ready": container.embedding_health.is_ready,
        "embedding_pending": container.embedding_health.pending,
        "embedding_error": container.embedding_health.error,
        "llm_configured": cfg.preprocessing.enabled and bool(cfg.preprocessing.model),
        "llm_ready": container.llm_health.is_ready,
        "llm_pending": container.llm_health.pending,
        "llm_error": container.llm_health.error,
        **kwargs,
    }


# ── Index ──────────────────────────────────────────────────────────────────────

_FAVICON_SVG = (
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">'
    '<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">'
    '<stop offset="0%" stop-color="#0da2e7"/>'
    '<stop offset="100%" stop-color="#7c3aed"/>'
    '</linearGradient></defs>'
    '<rect width="24" height="24" rx="5" fill="url(#g)"/>'
    '<path d="M13 10V3L4 14h7v7l9-11h-7z" fill="none" stroke="white"'
    ' stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"/>'
    '</svg>'
)


@router.get("/favicon.svg")
async def favicon_svg():
    return Response(content=_FAVICON_SVG, media_type="image/svg+xml",
                    headers={"Cache-Control": "public, max-age=86400"})


@router.get("/favicon.ico")
async def favicon():
    return RedirectResponse("/favicon.svg", status_code=301)


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


@router.post("/sync-all")
async def sync_all_sources(background_tasks: BackgroundTasks):
    sources = await container.config_store.get_all()
    for source in sources:
        if source.sync_status != "syncing":
            background_tasks.add_task(container.sync_service.sync, source.id)
    return RedirectResponse("/", status_code=303)


@router.post("/sources/{source_id}/sync/pause")
async def sync_pause(source_id: str):
    source = await container.config_store.get_by_id(source_id)
    if source and source.sync_status == "syncing":
        container.sync_control.pause(source_id)
    return JSONResponse({"ok": True})


@router.post("/sources/{source_id}/sync/resume")
async def sync_resume(source_id: str):
    container.sync_control.resume(source_id)
    return JSONResponse({"ok": True})


@router.post("/sources/{source_id}/sync/cancel")
async def sync_cancel(source_id: str):
    container.sync_control.request_cancel(source_id)
    return RedirectResponse("/", status_code=303)


@router.get("/health")
async def health_check():
    from app.config import get_config
    cfg = get_config()
    return JSONResponse({
        "qdrantReady": container.health.is_ready,
        "qdrantPending": container.health.pending,
        "qdrantRetry": container.health.retry_attempt,
        "qdrantMaxRetries": container.health.max_retries,
        "embeddingReady": container.embedding_health.is_ready,
        "embeddingPending": container.embedding_health.pending,
        "llmConfigured": cfg.preprocessing.enabled and bool(cfg.preprocessing.model),
        "llmReady": container.llm_health.is_ready,
        "llmPending": container.llm_health.pending,
    })


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
            "syncErrorPhase": s.sync_error_phase,
            "syncPaused": container.sync_control.is_paused(s.id),
        })
    return JSONResponse({"sources": result})


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
        parsed = json.loads(raw)
        # Support both flat array (legacy) and new {"sources": [...]} format
        items = parsed.get("sources", parsed) if isinstance(parsed, dict) else parsed
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
            request, sources=sources,
            import_message=f"Imported {imported} source(s). Add credentials at /credentials, then edit each source to assign them before syncing.",
        ))
    except Exception as exc:
        sources = await container.config_store.get_all()
        return templates.TemplateResponse(request, "index.html", _ctx(
            request, sources=sources,
            import_error=f"Import failed: {exc}",
        ))


# ── Credentials ───────────────────────────────────────────────────────────────

_CREDENTIAL_FIELDS = {"Pat", "Token", "Password", "ApiKeyValue"}


@router.get("/credentials")
async def credentials_get(request: Request, notice: str = ""):
    credentials = await container.secrets_store.list_all()
    sources = await container.config_store.get_all()
    cred_usage = {
        c.id: container.secrets_store.sources_using(c.id, sources)
        for c in credentials
    }

    # Names referenced in source configs but not yet created in the store.
    existing_names = {c.name for c in credentials}
    missing: dict[str, list[str]] = {}
    for source in sources:
        for field in _CREDENTIAL_FIELDS:
            ref = source.config.get(field, "")
            if ref and ref not in existing_names:
                missing.setdefault(ref, []).append(source.name)

    return templates.TemplateResponse(request, "credentials.html", _ctx(
        request,
        credentials=credentials,
        cred_usage=cred_usage,
        missing_credentials=missing,
        notice=notice,
    ))


@router.post("/credentials/create")
async def credentials_create(
    name: str = Form(...),
    note: str = Form(""),
    value: str = Form(...),
):
    name = name.strip()
    value = value.strip()
    if not name or not value:
        return RedirectResponse("/credentials?notice=error_empty", status_code=303)
    if "/" in name:
        return RedirectResponse("/credentials?notice=error_invalid_name", status_code=303)
    try:
        await container.secrets_store.create(name, note, value)
    except ValueError:
        return RedirectResponse("/credentials?notice=error_duplicate", status_code=303)
    except Exception:
        return RedirectResponse("/credentials?notice=error_unexpected", status_code=303)
    return RedirectResponse("/credentials?notice=created", status_code=303)


@router.post("/credentials/{cred_name}/edit")
async def credentials_edit(
    cred_name: str,
    name: str = Form(...),
    note: str = Form(""),
    value: str = Form(""),
):
    name = name.strip()
    if not name:
        return RedirectResponse("/credentials?notice=error_empty", status_code=303)
    if "/" in name:
        return RedirectResponse("/credentials?notice=error_invalid_name", status_code=303)
    try:
        old_name = await container.secrets_store.update(cred_name, name, note, value.strip() or None)
    except ValueError:
        return RedirectResponse("/credentials?notice=error_duplicate", status_code=303)
    except Exception:
        return RedirectResponse("/credentials?notice=error_unexpected", status_code=303)
    if not old_name:
        return RedirectResponse("/credentials?notice=error_not_found", status_code=303)
    if old_name != name:
        await container.config_store.rename_credential_references(old_name, name)
    return RedirectResponse("/credentials?notice=updated", status_code=303)


@router.post("/credentials/{cred_name}/delete")
async def credentials_delete(cred_name: str):
    try:
        await container.secrets_store.delete(cred_name)
    except Exception:
        return RedirectResponse("/credentials?notice=error_unexpected", status_code=303)
    return RedirectResponse("/credentials?notice=deleted", status_code=303)


# ── Settings ───────────────────────────────────────────────────────────────────

def _reload_embedding_services(cfg) -> None:
    """Reinitialize all embedding-dependent services in the container after a config change."""
    from app.rag.chunker import TextChunker
    from app.rag.embedding import EmbeddingService
    from app.rag.indexer import DocumentIndexer
    from app.rag.search import SearchService
    from app.memory.service import MemoryService

    new_embedding = EmbeddingService(cfg)
    new_chunker = TextChunker(cfg)

    container.search_service = SearchService(container.vector_store, new_embedding)
    container.memory_service = MemoryService(container.vector_store, new_embedding)

    # Patch the indexer held inside sync_service so subsequent syncs use the new model
    new_indexer = DocumentIndexer(container.vector_store, new_embedding, new_chunker)
    container.sync_service._indexer = new_indexer


def _reload_preprocessing_service(cfg) -> None:
    from app.rag.preprocessor import DocumentPreprocessor
    new_preprocessor = DocumentPreprocessor(cfg)
    container.preprocessor = new_preprocessor
    container.sync_service._preprocessor = new_preprocessor


@router.get("/settings")
async def settings_get(request: Request, notice: str = ""):
    from app.config import get_config, get_config_path
    cfg = get_config()
    return templates.TemplateResponse(request, "settings.html", _ctx(
        request,
        cfg=cfg,
        config_path=get_config_path(),
        notice=notice,
    ))


@router.post("/settings/embedding")
async def settings_save_embedding(
    model: str = Form(...),
    base_url: str = Form(""),
    dimensions: int = Form(768),
    max_input_chars: int = Form(8000),
):
    from app.config import AppConfig, EmbeddingConfig, get_config, save_config
    from app.models import CollectionNames

    old_cfg = get_config()
    new_embedding = EmbeddingConfig(
        model=model,
        base_url=base_url,
        dimensions=dimensions,
        max_input_chars=max_input_chars,
    )
    new_cfg = AppConfig(
        embedding=new_embedding,
        qdrant=old_cfg.qdrant,
        chunking=old_cfg.chunking,
        preprocessing=old_cfg.preprocessing,
        sources_file_path=old_cfg.sources_file_path,
    )

    embedding_changed = (
        old_cfg.embedding.model != model
        or old_cfg.embedding.dimensions != dimensions
        or old_cfg.embedding.base_url != base_url
    )

    save_config(new_cfg)

    if embedding_changed:
        for col in CollectionNames.ALL:
            try:
                if await container.vector_store.collection_exists(col):
                    await container.vector_store.delete_collection(col)
            except Exception:
                pass
        await container.config_store.reset_all_sync_status("needs-reindex")
        _reload_embedding_services(new_cfg)
        return RedirectResponse("/settings?notice=embedding_saved_dropped", status_code=303)

    _reload_embedding_services(new_cfg)
    return RedirectResponse("/settings?notice=embedding_saved", status_code=303)


@router.post("/settings/qdrant")
async def settings_save_qdrant(
    qdrant_host: str = Form("localhost"),
    qdrant_port: int = Form(6333),
):
    from app.config import AppConfig, get_config, QdrantConfig, save_config

    old_cfg = get_config()
    new_cfg = AppConfig(
        embedding=old_cfg.embedding,
        qdrant=QdrantConfig(host=qdrant_host, port=qdrant_port),
        chunking=old_cfg.chunking,
        preprocessing=old_cfg.preprocessing,
        sources_file_path=old_cfg.sources_file_path,
    )
    save_config(new_cfg)
    return RedirectResponse("/settings?notice=qdrant_saved", status_code=303)


@router.post("/settings/verify/qdrant")
async def settings_verify_qdrant(
    qdrant_host: str = Form("localhost"),
    qdrant_port: int = Form(6333),
):
    try:
        from qdrant_client import AsyncQdrantClient
        client = AsyncQdrantClient(host=qdrant_host, port=qdrant_port, timeout=5, check_compatibility=False)
        collections = await client.get_collections()
        count = len(collections.collections)
        await client.close()
        return JSONResponse({"ok": True, "message": f"Connected — {count} collection(s)"})
    except Exception as exc:
        return JSONResponse({"ok": False, "message": str(exc)})


@router.post("/settings/verify/embedding")
async def settings_verify_embedding(
    model: str = Form(...),
    base_url: str = Form(""),
    dimensions: int = Form(768),
    max_input_chars: int = Form(8000),
):
    try:
        from app.config import AppConfig, EmbeddingConfig, get_config
        from app.rag.embedding import EmbeddingService
        cfg = get_config()
        test_cfg = AppConfig(
            embedding=EmbeddingConfig(
                model=model,
                base_url=base_url,
                dimensions=dimensions,
                max_input_chars=max_input_chars,
            ),
            qdrant=cfg.qdrant,
            chunking=cfg.chunking,
            sources_file_path=cfg.sources_file_path,
        )
        svc = EmbeddingService(test_cfg)
        vector = await svc.embed("connection test")
        return JSONResponse({"ok": True, "message": f"OK — model returned {len(vector)}-dim vector"})
    except Exception as exc:
        return JSONResponse({"ok": False, "message": str(exc)})


@router.post("/settings/preprocessing")
async def settings_save_preprocessing(request: Request):
    from app.config import AppConfig, PreprocessingConfig, get_config, save_config, _PREPROCESS_SOURCE_TYPE_DEFAULTS

    form = await request.form()
    enabled = form.get("enabled", "")
    base_url = form.get("base_url", "")
    model = form.get("model", "")
    system_prompt = form.get("system_prompt", "")

    source_types: dict[str, bool] = {}
    for key in _PREPROCESS_SOURCE_TYPE_DEFAULTS:
        source_types[key] = form.get(f"source_type_{key.replace('-', '_')}", "") == "on"

    old_cfg = get_config()
    new_cfg = AppConfig(
        embedding=old_cfg.embedding,
        qdrant=old_cfg.qdrant,
        chunking=old_cfg.chunking,
        preprocessing=PreprocessingConfig(
            enabled=enabled == "on",
            base_url=base_url,
            model=model,
            system_prompt=system_prompt,
            source_types=source_types,
        ),
        sources_file_path=old_cfg.sources_file_path,
    )
    save_config(new_cfg)
    _reload_preprocessing_service(new_cfg)
    return RedirectResponse("/settings?notice=preprocessing_saved", status_code=303)


@router.post("/settings/verify/preprocessing")
async def settings_verify_preprocessing(
    base_url: str = Form(""),
    model: str = Form(""),
    system_prompt: str = Form(""),
):
    try:
        from app.config import AppConfig, PreprocessingConfig, get_config
        from app.models import SourceDocument
        from app.rag.preprocessor import DocumentPreprocessor

        cfg = get_config()
        test_cfg = AppConfig(
            embedding=cfg.embedding,
            qdrant=cfg.qdrant,
            chunking=cfg.chunking,
            preprocessing=PreprocessingConfig(
                enabled=True,
                base_url=base_url,
                model=model,
                system_prompt=system_prompt,
            ),
            sources_file_path=cfg.sources_file_path,
        )
        preprocessor = DocumentPreprocessor(test_cfg)
        test_doc = SourceDocument(
            id="verify-test",
            text="This is a connectivity test. Please respond with a brief acknowledgement.",
        )
        result = await preprocessor.preprocess_documents([test_doc])
        summary = result[0].text
        return JSONResponse({"ok": True, "message": f"OK — model responded ({len(summary)} chars)"})
    except Exception as exc:
        return JSONResponse({"ok": False, "message": str(exc)})


# ── Settings: Danger zone ─────────────────────────────────────────────────────

@router.post("/settings/delete-all-sources")
async def delete_all_sources():
    if not container.health.is_ready:
        return RedirectResponse("/settings?notice=qdrant_offline", status_code=303)
    sources = await container.config_store.get_all()
    for source in sources:
        col = collection_for(source)
        try:
            filt = Filter(must=[
                FieldCondition(key=PayloadKeys.tag("source_id"), match=MatchValue(value=source.id))
            ])
            await container.vector_store.delete_by_filter(col, filt)
        except Exception:
            pass
        await container.config_store.delete(source.id)
    return RedirectResponse("/settings?notice=sources_deleted", status_code=303)


@router.post("/settings/delete-all-experiences")
async def delete_all_experiences():
    from app.models import CollectionNames
    try:
        await container.vector_store.delete_collection(CollectionNames.EXPERIENCE)
        await container.vector_store.create_collection(CollectionNames.EXPERIENCE)
    except Exception:
        pass
    return RedirectResponse("/settings?notice=experiences_deleted", status_code=303)


@router.post("/settings/clean-source-embeddings")
async def clean_source_embeddings():
    from app.models import CollectionNames
    for col in CollectionNames.ALL:
        if col == CollectionNames.EXPERIENCE:
            continue
        try:
            if await container.vector_store.collection_exists(col):
                await container.vector_store.delete_collection(col)
        except Exception:
            pass
    await container.config_store.reset_all_sync_status("needs-reindex")
    return RedirectResponse("/settings?notice=source_embeddings_cleaned", status_code=303)


@router.post("/settings/clean-experience-embeddings")
async def clean_experience_embeddings():
    from app.models import CollectionNames
    try:
        await container.vector_store.delete_collection(CollectionNames.EXPERIENCE)
        await container.vector_store.create_collection(CollectionNames.EXPERIENCE)
    except Exception:
        pass
    return RedirectResponse("/settings?notice=experience_embeddings_cleaned", status_code=303)


# ── Sources: Preview ───────────────────────────────────────────────────────────

@router.post("/sources/preview")
async def sources_preview(request: Request):
    import asyncio
    import traceback

    try:
        form = await request.form()
        source = _build_source_from_form(form)
        if not source.type:
            return JSONResponse({"error": "Source type could not be determined from the form. Please reload the page and try again."}, status_code=400)

        if source.get_config(ConfigKeys.PROVIDER) == "manual" and source.get_config(ConfigKeys.MANUAL_TYPE) == "upload":
            manual_file = form.get("manual_file")
            if manual_file and getattr(manual_file, "filename", None):
                # New file dropped/chosen — extract and override
                source.config[ConfigKeys.CONTENT] = await _extract_file_text(manual_file)
                if not source.get_config(ConfigKeys.TITLE):
                    source.config[ConfigKeys.TITLE] = manual_file.filename
            elif not source.get_config(ConfigKeys.CONTENT):
                return JSONResponse({"error": "No file provided. Choose or drop a file to preview."}, status_code=400)

        factory = container.sync_service._factory
        impl = factory.create(source)
        docs = await asyncio.wait_for(impl.preview_documents(), timeout=30.0)
        sample = docs[:5]
        # File-tree sources embed the true matched count on the first doc so
        # the reported total reflects all matched files, not just the 5 whose
        # content was fetched.
        matched_total_str = docs[0].properties.pop("__matched_total__", None) if docs else None
        reported_total = int(matched_total_str) if matched_total_str else len(docs)
        return JSONResponse({
            "total": reported_total,
            "docs": [
                {
                    "text": d.text[:500] if _is_text_displayable(d.text) else None,
                    "tags": d.tags,
                    "properties": d.properties,
                }
                for d in sample
            ],
        })
    except asyncio.TimeoutError:
        return JSONResponse({"error": "Preview timed out after 30 s — try a smaller query or limit."}, status_code=408)
    except Exception as exc:
        return JSONResponse({"error": f"{type(exc).__name__}: {exc}"}, status_code=500)


# ── Sources: Create ────────────────────────────────────────────────────────────

@router.get("/sources/create")
async def sources_create_get(request: Request, type: str = "", provider: str = ""):
    _meta_map = {m.type: m for m in SOURCE_TYPE_META}
    meta = _meta_map.get(type)

    # Step 3 — configure a specific type
    if type:
        credentials = await container.secrets_store.list_all()
        return templates.TemplateResponse(request, "sources/create.html", _ctx(
            request,
            step="configure",
            selected_type=type,
            selected_provider=meta.provider if meta else "",
            type_label=meta.label if meta else type,
            provider_label=PROVIDERS.get(meta.provider if meta else "", {}).get("label", ""),
            source=SourceDefinition(type=type, name=""),
            credentials=credentials,
        ))

    # Step 2 — pick a type within a platform
    if provider:
        types_in_provider = [m for m in SOURCE_TYPE_META if m.provider == provider]
        platform = PLATFORMS.get(provider, {})
        return templates.TemplateResponse(request, "sources/create.html", _ctx(
            request,
            step="pick_type",
            selected_type="",
            selected_provider=provider,
            platform_label=platform.get("label", provider),
            source_types=types_in_provider,
            source=SourceDefinition(type="", name=""),
        ))

    # Only one platform — skip the picker and go straight to the type list
    return RedirectResponse("/sources/create?provider=ado", status_code=302)


@router.post("/sources/create")
async def sources_create_post(request: Request, background_tasks: BackgroundTasks):
    form = await request.form()
    source = _build_source_from_form(form)

    if source.get_config(ConfigKeys.DOC_TYPE) == "upload":
        doc_file = form.get("doc_file")
        if doc_file and getattr(doc_file, "filename", None):
            source.config[ConfigKeys.CONTENT] = await _extract_pdf_text(doc_file)
            source.config[ConfigKeys.TITLE] = doc_file.filename

    if source.get_config(ConfigKeys.PROVIDER) == "manual" and source.get_config(ConfigKeys.MANUAL_TYPE) == "upload":
        manual_file = form.get("manual_file")
        if manual_file and getattr(manual_file, "filename", None):
            source.config[ConfigKeys.CONTENT] = await _extract_file_text(manual_file)
            if not source.get_config(ConfigKeys.TITLE):
                source.config[ConfigKeys.TITLE] = manual_file.filename

    await container.config_store.save(source)
    if form.get("action", "save_sync") == "save_sync":
        background_tasks.add_task(container.sync_service.sync, source.id)
    return RedirectResponse("/", status_code=303)


# ── Sources: Edit ──────────────────────────────────────────────────────────────

@router.get("/sources/{source_id}/edit")
async def sources_edit_get(request: Request, source_id: str):
    source = await container.config_store.get_by_id(source_id)
    if not source:
        return RedirectResponse("/")
    meta = next((m for m in SOURCE_TYPE_META if m.type == source.type), None)
    type_label = meta.label if meta else source.type
    provider_label = PROVIDERS.get(meta.provider, {}).get("label", "") if meta else ""
    credentials = await container.secrets_store.list_all()
    return templates.TemplateResponse(request, "sources/edit.html", _ctx(
        request,
        source=source,
        type_label=type_label,
        provider_label=provider_label,
        credentials=credentials,
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
    updated.sync_error_phase = None

    if updated.get_config(ConfigKeys.DOC_TYPE) == "upload":
        doc_file = form.get("doc_file")
        if doc_file and getattr(doc_file, "filename", None):
            updated.config[ConfigKeys.CONTENT] = await _extract_pdf_text(doc_file)
            updated.config[ConfigKeys.TITLE] = doc_file.filename
        else:
            # Keep existing content if no new file uploaded
            updated.config[ConfigKeys.CONTENT] = existing.get_config(ConfigKeys.CONTENT)
            updated.config[ConfigKeys.TITLE] = existing.get_config(ConfigKeys.TITLE)

    if updated.get_config(ConfigKeys.PROVIDER) == "manual" and updated.get_config(ConfigKeys.MANUAL_TYPE) == "upload":
        manual_file = form.get("manual_file")
        if manual_file and getattr(manual_file, "filename", None):
            updated.config[ConfigKeys.CONTENT] = await _extract_pdf_text(manual_file)
            if not updated.get_config(ConfigKeys.TITLE):
                updated.config[ConfigKeys.TITLE] = manual_file.filename
        else:
            # Keep existing content if no new file uploaded
            updated.config[ConfigKeys.CONTENT] = existing.get_config(ConfigKeys.CONTENT)
            if not updated.get_config(ConfigKeys.TITLE):
                updated.config[ConfigKeys.TITLE] = existing.get_config(ConfigKeys.TITLE)

    if existing.config != updated.config:
        collection = collection_for(updated)
        try:
            filt = Filter(must=[
                FieldCondition(key=PayloadKeys.tag("source_id"), match=MatchValue(value=source_id))
            ])
            await container.vector_store.delete_by_filter(collection, filt)
        except Exception:
            pass  # Qdrant may be offline; config save proceeds regardless

    await container.config_store.save(updated)
    if form.get("action") == "save_sync":
        background_tasks.add_task(container.sync_service.sync, source_id)
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
        collection = collection_for(source)
        try:
            filt = Filter(must=[
                FieldCondition(key=PayloadKeys.tag("source_id"), match=MatchValue(value=source.id))
            ])
            await container.vector_store.delete_by_filter(collection, filt)
        except Exception:
            pass
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

    collection = collection_for(source)
    scroll_filter = Filter(must=[
        FieldCondition(key=PayloadKeys.tag("source_id"), match=MatchValue(value=source.id))
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
                "chunk_index": int(payload[PayloadKeys.CHUNK_INDEX]) if payload.get(PayloadKeys.CHUNK_INDEX) is not None else None,
                "total_chunks": int(payload[PayloadKeys.TOTAL_CHUNKS]) if payload.get(PayloadKeys.TOTAL_CHUNKS) is not None else None,
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
async def experience_list(request: Request, q: str = "", offset: Optional[str] = None):
    import logging
    log = logging.getLogger(__name__)

    total = await container.memory_service.count()
    search_error: str | None = None

    if q.strip():
        try:
            results = await container.memory_service.retrieve(q.strip(), top_k=20)
        except Exception as exc:
            log.exception("Experience search failed for query %r", q)
            results = []
            search_error = str(exc)
        entries = [{"id": "", "situation": r["situation"], "guidance": r["guidance"], "created_at": "", "score": r["score"]} for r in results]
        next_offset = None
        has_prev = False
    else:
        try:
            entries, next_offset = await container.memory_service.get_all_paginated(limit=20, offset=offset)
        except Exception:
            entries, next_offset = [], None
        for e in entries:
            e["score"] = None
        has_prev = offset is not None
        search_error = None

    return templates.TemplateResponse(request, "experience/list.html", _ctx(
        request,
        entries=entries,
        next_offset=next_offset,
        has_prev=has_prev,
        total=total,
        q=q,
        search_error=search_error,
    ))


@router.get("/experience/create")
async def experience_create_get(request: Request):
    return templates.TemplateResponse(request, "experience/create.html", _ctx(request))


@router.post("/experience/add")
async def experience_add(
    situation: str = Form(...),
    guidance: str = Form(...),
):
    situation = situation.strip()
    guidance = guidance.strip()
    if situation and guidance:
        try:
            await container.memory_service.remember(situation, guidance)
        except Exception:
            pass  # Qdrant offline — silently skip; user sees "Qdrant offline" in sidebar
    return RedirectResponse("/experience", status_code=303)


@router.post("/experience/{entry_id}/delete")
async def experience_delete(entry_id: str):
    try:
        await container.memory_service.delete(entry_id)
    except Exception:
        pass  # Qdrant offline — proceed to redirect
    return RedirectResponse("/experience", status_code=303)


@router.get("/experience/map")
async def experience_map(request: Request):
    total = await container.memory_service.count()
    return templates.TemplateResponse(request, "experience/map.html", _ctx(
        request,
        total=total,
    ))


@router.get("/map")
async def sources_map(request: Request):
    sources = await container.config_store.get_all()
    total = sum(1 for s in sources if s.sync_status == "completed")
    return templates.TemplateResponse(request, "map.html", _ctx(request, total=total))


@router.get("/api/map-data")
async def sources_map_data(method: str = "pca"):
    try:
        import numpy as np
        from sklearn.decomposition import PCA
    except ImportError:
        return JSONResponse({"error": "scikit-learn not installed. Run: uv pip install scikit-learn"}, status_code=500)

    SAMPLE_PER_SOURCE = 200

    sources = await container.config_store.get_all()
    all_vectors: list[list[float]] = []
    all_meta: list[dict] = []
    source_stats: list[dict] = []

    for source in sources:
        if source.sync_status != "completed":
            continue
        collection = collection_for(source)
        try:
            points, _ = await container.vector_store.client.scroll(
                collection_name=collection,
                scroll_filter=Filter(must=[FieldCondition(
                    key=PayloadKeys.tag("source_id"),
                    match=MatchValue(value=source.id),
                )]),
                limit=SAMPLE_PER_SOURCE,
                with_payload=True,
                with_vectors=True,
            )
        except Exception:
            continue

        count = 0
        for p in points:
            vector = p.vector
            if vector is None:
                continue
            if isinstance(vector, dict):
                vector = next(iter(vector.values()), None)
                if not vector:
                    continue
            payload = p.payload or {}
            title = payload.get(PayloadKeys.prop("title"), "") or ""
            text = payload.get(PayloadKeys.TEXT, "") or ""
            all_vectors.append(list(vector))
            all_meta.append({
                "source_id": source.id,
                "source_name": source.name,
                "source_type": source.type,
                "title": title[:80],
                "text": text[:120],
            })
            count += 1

        if count:
            source_stats.append({"id": source.id, "name": source.name, "type": source.type, "sampled": count})

    if len(all_vectors) < 2:
        return JSONResponse({"points": [], "sources": [], "total": len(all_vectors),
                             "note": f"Need at least 2 indexed documents (have {len(all_vectors)})."})

    vectors = np.array(all_vectors, dtype=float)

    if method == "umap":
        try:
            import umap as umap_lib
        except ImportError:
            return JSONResponse({"error": "umap-learn not installed. Run: uv pip install umap-learn"}, status_code=500)
        if len(all_vectors) < 4:
            return JSONResponse({"error": "UMAP needs at least 4 points.", "points": []}, status_code=400)
        n_neighbors = min(15, len(all_vectors) - 1)
        def _run_umap():
            return umap_lib.UMAP(
                n_components=2, n_neighbors=n_neighbors, min_dist=0.1,
                random_state=42, low_memory=False, n_epochs=200,
            ).fit_transform(vectors)
        try:
            coords = await asyncio.get_event_loop().run_in_executor(None, _run_umap)
        except Exception as exc:
            return JSONResponse({"error": f"UMAP failed: {exc}", "points": []}, status_code=500)
        axis_labels = ("UMAP 1", "UMAP 2")
    else:
        n = min(2, len(all_vectors), vectors.shape[1])
        def _run_pca():
            return PCA(n_components=n).fit_transform(vectors)
        try:
            coords = await asyncio.get_event_loop().run_in_executor(None, _run_pca)
        except Exception as exc:
            return JSONResponse({"error": f"PCA failed: {exc}", "points": []}, status_code=500)
        axis_labels = ("PC 1", "PC 2")

    points = []
    for i, meta in enumerate(all_meta):
        points.append({
            **meta,
            "x": float(coords[i, 0]),
            "y": float(coords[i, 1]) if coords.shape[1] >= 2 else 0.0,
        })

    return JSONResponse({
        "points": points,
        "sources": source_stats,
        "total": len(points),
        "axis_labels": list(axis_labels),
    })


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
            "guidance": e["guidance"],
            "created_at": e["created_at"],
        })
    return JSONResponse({"points": points})


@router.post("/sources/{source_id}/copy")
async def sources_copy(source_id: str):
    source = await container.config_store.get_by_id(source_id)
    if not source:
        return RedirectResponse("/", status_code=303)
    copy = source.model_copy(update={
        "id": str(uuid.uuid4()),
        "name": source.name + " Copy",
        "sync_status": "idle",
        "sync_error": None,
        "sync_error_phase": None,
        "last_synced_at": None,
    })
    await container.config_store.save(copy)
    return RedirectResponse("/", status_code=303)


@router.post("/sources/reorder")
async def sources_reorder(request: Request):
    data = await request.json()
    ids = data.get("ids", [])
    if not isinstance(ids, list):
        return JSONResponse({"error": "ids must be a list"}, status_code=400)
    await container.config_store.reorder(ids)
    return JSONResponse({"ok": True})


# ── Helpers ────────────────────────────────────────────────────────────────────

def _is_text_displayable(text: str) -> bool:
    """Return False for binary or mostly non-printable content."""
    if not text:
        return False
    if '\x00' in text:
        return False
    sample = text[:200]
    non_printable = sum(1 for c in sample if ord(c) < 32 and c not in '\n\r\t')
    return non_printable / len(sample) < 0.1


def _get_form_str(form, *keys: str) -> str:
    """Return first non-None form value; treats empty string as a valid value only for the first key."""
    for k in keys:
        v = form.get(k)
        if v is not None:
            return str(v)
    return ""


def _build_source_from_form(form) -> SourceDefinition:
    source_type = _get_form_str(form, "source_type", "Source.Type")
    name = _get_form_str(form, "name", "Source.Name")
    source_id = _get_form_str(form, "Source.Id") or str(uuid.uuid4())

    config: dict[str, str] = {}

    def _set(key: str, *form_keys: str, default: str = "") -> None:
        for fk in form_keys:
            val = form.get(fk)
            if val:
                config[key] = str(val)
                return
        if default:
            config[key] = default

    _set(ConfigKeys.PROVIDER, "ConfigProvider", default="ado")

    _set(ConfigKeys.BASE_URL, "ConfigBaseUrl")
    _set(ConfigKeys.AUTH_TYPE, "ConfigAuthType")
    _set(ConfigKeys.API_VERSION, "ConfigApiVersion")
    _set(ConfigKeys.VERIFY_SSL, "ConfigVerifySSL")

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

    if source_type == SourceTypes.WORK_ITEM_QUERY:
        item_types = form.getlist("ConfigItemTypes")
        if item_types:
            config[ConfigKeys.ITEM_TYPES] = ",".join(item_types)
        _set(ConfigKeys.AREA_PATH, "ConfigAreaPath")
        _set(ConfigKeys.ITERATION_PATH, "ConfigIterationPath")
        _set(ConfigKeys.QUERY, "ConfigQuery")  # advanced override
        _set(ConfigKeys.FIELDS, "ConfigFields")

    if source_type == SourceTypes.REQUIREMENTS:
        _set(ConfigKeys.REQ_TYPE, "ConfigReqType", default="filters")
        req_type = config.get(ConfigKeys.REQ_TYPE, "filters")
        if req_type == "filters":
            item_types = form.getlist("ConfigItemTypes")
            if item_types:
                config[ConfigKeys.ITEM_TYPES] = ",".join(item_types)
            _set(ConfigKeys.AREA_PATH, "ConfigAreaPath")
            _set(ConfigKeys.ITERATION_PATH, "ConfigIterationPath")
        elif req_type == "custom":
            _set(ConfigKeys.QUERY, "ConfigQuery")
            _set(ConfigKeys.FIELDS, "ConfigFields")
        elif req_type == "repo":
            _set(ConfigKeys.REPOSITORY, "ConfigRepository")
            _set(ConfigKeys.BRANCH, "ConfigBranch")
            _set(ConfigKeys.GLOB_PATTERNS, "ConfigGlobPatterns", default="**/*.md")

    if source_type == SourceTypes.TEST_CASE:
        _set(ConfigKeys.TC_TYPE, "ConfigTcType", default="filters")
        tc_type = config.get(ConfigKeys.TC_TYPE, "filters")
        if tc_type == "filters":
            item_types = form.getlist("ConfigItemTypes")
            if item_types:
                config[ConfigKeys.ITEM_TYPES] = ",".join(item_types)
            _set(ConfigKeys.AREA_PATH, "ConfigAreaPath")
            _set(ConfigKeys.ITERATION_PATH, "ConfigIterationPath")
        elif tc_type == "custom":
            _set(ConfigKeys.QUERY, "ConfigQuery")
            _set(ConfigKeys.FIELDS, "ConfigFields")
        elif tc_type == "repo":
            _set(ConfigKeys.REPOSITORY, "ConfigRepository")
            _set(ConfigKeys.BRANCH, "ConfigBranch")
            _set(ConfigKeys.GLOB_PATTERNS, "ConfigGlobPatterns", default="**/*.md")

    if source_type == SourceTypes.CODE_REPO:
        _set(ConfigKeys.REPOSITORY, "ConfigRepository")
        _set(ConfigKeys.BRANCH, "ConfigBranch")
        _set(ConfigKeys.GLOB_PATTERNS, "ConfigGlobPatterns", default="**/*.cs")

    if source_type == SourceTypes.TEST_CODE_REPO:
        _set(ConfigKeys.REPOSITORY, "ConfigRepository")
        _set(ConfigKeys.BRANCH, "ConfigBranch")
        _set(ConfigKeys.GLOB_PATTERNS, "ConfigGlobPatterns", default="**/*Tests.cs,**/*Spec.cs,**/*test*.py,**/*.spec.ts,**/*.test.ts")

    if source_type == SourceTypes.PIPELINE_BUILD:
        _set(ConfigKeys.BUILD_TYPE, "ConfigBuildType", default="build")
        build_type = config.get(ConfigKeys.BUILD_TYPE, "build")
        if build_type == "build":
            _set(ConfigKeys.PIPELINE_ID, "ConfigPipelineId")
            _set(ConfigKeys.LAST_N_BUILDS, "ConfigLastNBuilds", default="5")
        elif build_type == "release":
            _set(ConfigKeys.RELEASE_DEFINITION_ID, "ConfigReleaseDefinitionId")
            _set(ConfigKeys.LAST_N_RELEASES, "ConfigLastNReleases", default="5")

    if source_type == SourceTypes.DOCUMENTATION:
        _set(ConfigKeys.DOC_TYPE, "ConfigDocType", default="wiki")
        if config.get(ConfigKeys.DOC_TYPE) == "wiki":
            _set(ConfigKeys.WIKI_NAME, "ConfigWikiName")
            _set(ConfigKeys.PATH_FILTER, "ConfigPathFilter")
        elif config.get(ConfigKeys.DOC_TYPE) == "repo":
            _set(ConfigKeys.REPOSITORY, "ConfigRepository")
            _set(ConfigKeys.BRANCH, "ConfigBranch")
            _set(ConfigKeys.GLOB_PATTERNS, "ConfigGlobPatterns", default="**/*.md")


    if source_type == SourceTypes.TEST_RESULTS:
        _set(ConfigKeys.LAST_N_RUNS, "ConfigLastNRuns", default="10")
        _set(ConfigKeys.RESULTS_PER_RUN, "ConfigResultsPerRun", default="200")

    if source_type == SourceTypes.GIT_COMMITS:
        _set(ConfigKeys.REPOSITORY, "ConfigRepository")
        _set(ConfigKeys.BRANCH, "ConfigBranch")
        _set(ConfigKeys.LAST_N_COMMITS, "ConfigLastNCommits", default="100")

    if config.get(ConfigKeys.PROVIDER, "ado") == "custom":
        _set(ConfigKeys.URL, "ConfigUrl")
        _set(ConfigKeys.HTTP_METHOD, "ConfigHttpMethod", default="GET")
        _set(ConfigKeys.ITEMS_PATH, "ConfigItemsPath")
        _set(ConfigKeys.TITLE_FIELD, "ConfigTitleField", default="title")
        _set(ConfigKeys.CONTENT_FIELDS, "ConfigContentFields")

    if config.get(ConfigKeys.PROVIDER, "ado") == "manual":
        _set(ConfigKeys.MANUAL_TYPE, "ConfigManualType", default="text")
        _set(ConfigKeys.TITLE, "ConfigManualTitle")
        if config.get(ConfigKeys.MANUAL_TYPE, "text") == "text":
            _set(ConfigKeys.CONTENT, "ConfigManualText")
        else:
            # Carry existing content when no new file is uploaded (edit + preview flows)
            _set(ConfigKeys.CONTENT, "ConfigManualExistingContent")

    return SourceDefinition(id=source_id, type=source_type, name=name, config=config)


