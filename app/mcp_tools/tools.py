from __future__ import annotations

import json
from typing import Optional

from mcp.server.fastmcp import FastMCP

from app.models import CollectionNames


def register_tools(mcp: FastMCP, search_service, memory_service) -> None:
    """Register all MCP tools. Called once at startup."""

    # ── Knowledge search tools ─────────────────────────────────────────────────

    def _make_search_tool(collection: str, name: str, description: str):
        @mcp.tool(name=name, description=description)
        async def _tool(
            query: str,
            top_k: int = 5,
            source_name: Optional[str] = None,
        ) -> str:
            tags = {"source_name": source_name} if source_name else None
            results = await search_service.search(collection, query, top_k, tags)
            return json.dumps([r.model_dump() for r in results], default=str)
        return _tool

    _make_search_tool(
        CollectionNames.MANUAL_DOCUMENTS,
        "search_manual_documents",
        "Semantic search over manually uploaded documents and text. "
        "Returns the most relevant passages for the given query.",
    )
    _make_search_tool(
        CollectionNames.ADO_WORK_ITEMS,
        "search_ado_workitems",
        "Semantic search over Azure DevOps work items (bugs, tasks, user stories). "
        "Optionally filter by source_name to target a specific source.",
    )
    _make_search_tool(
        CollectionNames.ADO_CODE,
        "search_ado_code",
        "Semantic search over Azure DevOps source code (classes, methods, functions). "
        "Finds relevant code units for the given query.",
    )
    _make_search_tool(
        CollectionNames.ADO_BUILDS,
        "search_ado_builds",
        "Semantic search over Azure DevOps pipeline build results and failure details.",
    )
    _make_search_tool(
        CollectionNames.ADO_REQUIREMENTS,
        "search_ado_requirements",
        "Semantic search over Azure DevOps requirements work items.",
    )
    _make_search_tool(
        CollectionNames.ADO_TEST_CASES,
        "search_ado_testcases",
        "Semantic search over Azure DevOps test cases including test steps.",
    )
    _make_search_tool(
        CollectionNames.ADO_WIKI,
        "search_ado_wiki",
        "Semantic search over Azure DevOps wiki pages and sections.",
    )
    _make_search_tool(
        CollectionNames.HTTP_PAGES,
        "search_http_pages",
        "Semantic search over indexed HTTP pages and JSON endpoints.",
    )

    # ── Experience tools ───────────────────────────────────────────────────────

    @mcp.tool(
        name="retrieve_experience",
        description=(
            "ALWAYS call this tool at the START of every new task, conversation, or user request. "
            "It recalls relevant past experience: user preferences, known mistakes and their fixes, "
            "prior decisions, codebase facts, and any context stored from previous sessions. "
            "This is critical for maintaining continuity and avoiding repeating the same errors. "
            "Pass a query describing the current task or topic."
        ),
    )
    async def retrieve_experience(query: str, top_k: int = 5) -> str:
        results = await memory_service.retrieve(query, top_k)
        if not results:
            return json.dumps({"experience": [], "note": "No relevant experience found."})
        return json.dumps({
            "experience": [
                {
                    "text": r.text,
                    "category": r.properties.get("category", "general"),
                    "importance": r.properties.get("importance", "3"),
                    "score": round(r.score, 3),
                }
                for r in results
            ]
        }, default=str)

    @mcp.tool(
        name="remember",
        description=(
            "ALWAYS use this tool to store any information worth retaining across sessions. "
            "Store: user preferences, past mistakes and their exact fixes, decisions made, "
            "important codebase facts, patterns the user cares about, things to avoid. "
            "Call this proactively whenever you learn something the user would want you to remember "
            "in a future conversation — do not wait to be asked. "
            "Categories: 'preference', 'mistake', 'fact', 'decision', 'context'. "
            "Importance: 1=low, 3=normal, 5=critical."
        ),
    )
    async def remember(
        content: str,
        category: str = "general",
        importance: int = 3,
    ) -> str:
        entry_id = await memory_service.remember(content, category, importance)
        return json.dumps({"status": "stored", "entry_id": entry_id, "category": category, "importance": importance})
