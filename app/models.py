from __future__ import annotations

from datetime import datetime
from enum import Enum
from typing import Optional
from pydantic import BaseModel, Field
import uuid


# ── Collection names ────────────────────────────────────────────────────────

class CollectionNames:
    MANUAL_DOCUMENTS = "conduit_manual_documents"  # kept for reference/migration only
    WORK_ITEMS       = "conduit_workitems"
    REQUIREMENTS     = "conduit_requirements"
    CODE             = "conduit_code"
    BUILDS           = "conduit_builds"
    TEST_CASES       = "conduit_testcases"
    DOCUMENTATION    = "conduit_documentation"
    TEST_RESULTS     = "conduit_test_results"
    COMMITS          = "conduit_commits"
    EXPERIENCE       = "conduit_experience"

    ALL: list[str] = [
        WORK_ITEMS, REQUIREMENTS, CODE, BUILDS,
        TEST_CASES, DOCUMENTATION, TEST_RESULTS, COMMITS,
        EXPERIENCE,
    ]


# ── Source types ─────────────────────────────────────────────────────────────

class SourceTypes:
    WORK_ITEM_QUERY = "workitem"
    REQUIREMENTS    = "requirements"
    TEST_CASE       = "test-case"
    CODE_REPO       = "code"
    PIPELINE_BUILD  = "pipeline-build"
    DOCUMENTATION   = "documentation"
    TEST_RESULTS    = "test-results"
    GIT_COMMITS     = "git-commits"


# ── Config keys ───────────────────────────────────────────────────────────────

class ConfigKeys:
    BASE_URL        = "BaseUrl"
    AUTH_TYPE       = "AuthType"
    API_VERSION     = "ApiVersion"
    PAT             = "Pat"
    TOKEN           = "Token"
    USERNAME        = "Username"
    PASSWORD        = "Password"
    DOMAIN          = "Domain"
    API_KEY_HEADER  = "ApiKeyHeader"
    API_KEY_VALUE   = "ApiKeyValue"
    TITLE           = "Title"
    CONTENT         = "Content"
    QUERY           = "Query"
    FIELDS          = "Fields"
    ITEM_TYPES      = "ItemTypes"
    AREA_PATH       = "AreaPath"
    ITERATION_PATH  = "IterationPath"
    REPOSITORY      = "Repository"
    BRANCH          = "Branch"
    GLOB_PATTERNS   = "GlobPatterns"
    PIPELINE_ID     = "PipelineId"
    LAST_N_BUILDS   = "LastNBuilds"
    DOC_TYPE        = "DocType"
    WIKI_NAME       = "WikiName"
    PATH_FILTER     = "PathFilter"
    STATUS_FILTER   = "StatusFilter"
    TOP             = "Top"
    LAST_N_RUNS     = "LastNRuns"
    RESULTS_PER_RUN = "ResultsPerRun"
    LAST_N_COMMITS  = "LastNCommits"
    URL             = "Url"
    CONTENT_TYPE    = "ContentType"
    PROVIDER        = "Provider"
    HTTP_METHOD     = "HttpMethod"
    ITEMS_PATH      = "ItemsPath"
    TITLE_FIELD     = "TitleField"
    CONTENT_FIELDS  = "ContentFields"
    MANUAL_TYPE     = "ManualType"
    VERIFY_SSL      = "VerifySSL"
    REQ_TYPE        = "ReqType"            # "wiql" | "repo" | "wiki"
    TC_TYPE         = "TcType"             # "wiql" | "repo"
    BUILD_TYPE      = "BuildType"          # "build" | "release"
    RELEASE_DEFINITION_ID = "ReleaseDefinitionId"
    LAST_N_RELEASES = "LastNReleases"


# ── Core domain models ────────────────────────────────────────────────────────

class SourceDefinition(BaseModel):
    id: str = Field(default_factory=lambda: str(uuid.uuid4()))
    type: str
    name: str
    last_synced_at: Optional[datetime] = None
    sync_status: str = "idle"   # idle | syncing | completed | failed
    sync_error: Optional[str] = None
    sync_error_phase: Optional[str] = None   # fetch | embed
    config: dict[str, str] = Field(default_factory=dict)

    model_config = {"populate_by_name": True}

    def get_config(self, key: str, default: str = "") -> str:
        return self.config.get(key, default)


class SearchResult(BaseModel):
    id: str
    score: float
    text: str
    tags: dict[str, str] = Field(default_factory=dict)
    properties: dict[str, str] = Field(default_factory=dict)


class SourceDocument(BaseModel):
    id: str
    text: str
    tags: dict[str, str] = Field(default_factory=dict)
    properties: dict[str, str] = Field(default_factory=dict)


class TextChunk(BaseModel):
    text: str
    index: int
    start_offset: int
    end_offset: int


class SyncProgress(BaseModel):
    phase: str = "fetching"   # fetching | embedding
    current: int = 0
    total: int = 0
    message: Optional[str] = None


# ── Payload key constants ─────────────────────────────────────────────────────

class PayloadKeys:
    TEXT           = "text"
    INDEXED_AT_MS  = "indexed_at_ms"
    SOURCE_DOC_ID  = "source_doc_id"
    CHUNK_INDEX    = "chunk_index"
    TOTAL_CHUNKS   = "total_chunks"
    TAG_PREFIX     = "tag_"
    PROP_PREFIX    = "prop_"

    @staticmethod
    def tag(key: str) -> str:
        return f"tag_{key}"

    @staticmethod
    def prop(key: str) -> str:
        return f"prop_{key}"


# ── Code parsing models ───────────────────────────────────────────────────────

class CodeUnitKind(str, Enum):
    FILE        = "File"
    NAMESPACE   = "Namespace"
    CLASS       = "Class"
    INTERFACE   = "Interface"
    RECORD      = "Record"
    STRUCT      = "Struct"
    ENUM        = "Enum"
    METHOD      = "Method"
    CONSTRUCTOR = "Constructor"
    PROPERTY    = "Property"
    FUNCTION    = "Function"
    TYPE        = "Type"
    SECTION     = "Section"


class CodeUnit(BaseModel):
    kind: CodeUnitKind
    name: str
    container_name: Optional[str] = None
    namespace: Optional[str] = None
    signature: Optional[str] = None
    is_public: bool = True
    doc_comment: Optional[str] = None
    full_text: str
    language: str
    file_path: str

    @property
    def enriched_text(self) -> str:
        parts: list[str] = []
        if self.namespace:
            parts.append(f"Namespace: {self.namespace}")
        kind_label = self.kind.value
        if self.container_name:
            parts.append(f"{kind_label}: {self.container_name}.{self.name}")
        else:
            parts.append(f"{kind_label}: {self.name}")
        if self.signature:
            parts.append(f"Signature: {self.signature}")
        parts.append(f"Language: {self.language}")
        parts.append(f"File: {self.file_path}")
        if self.doc_comment:
            parts.append(f"Docs: {self.doc_comment}")
        parts.append("")
        parts.append(self.full_text)
        return "\n".join(parts)

    def to_id_slug(self) -> str:
        import hashlib
        import re
        base = self.name.lower()
        if self.container_name:
            base = f"{self.container_name.lower()}-{base}"
        base = re.sub(r"[^a-z0-9]+", "-", base).strip("-")
        if self.signature:
            h = hashlib.md5(self.signature.encode()).hexdigest()[:8]
            base = f"{base}-{h}"
        return base[:80]
