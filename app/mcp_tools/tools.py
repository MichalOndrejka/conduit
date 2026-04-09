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
        "search_documents",
        "Semantic search over manually uploaded documents and text. "
        "Returns the most relevant passages for the given query.",
    )
    _make_search_tool(
        CollectionNames.WORK_ITEMS,
        "search_workitems",
        "Semantic search over work items (bugs, tasks, user stories, features). "
        "Optionally filter by source_name to target a specific source.",
    )
    _make_search_tool(
        CollectionNames.CODE,
        "search_code",
        "Semantic search over source code (classes, methods, functions). "
        "Finds relevant code units for the given query.",
    )
    _make_search_tool(
        CollectionNames.BUILDS,
        "search_builds",
        "Semantic search over pipeline build results and failure details.",
    )
    _make_search_tool(
        CollectionNames.TEST_CASES,
        "search_testcases",
        "Semantic search over test cases including test steps.",
    )
    _make_search_tool(
        CollectionNames.DOCUMENTATION,
        "search_documentation",
        "Semantic search over wiki pages and documentation sections.",
    )
    _make_search_tool(
        CollectionNames.PULL_REQUESTS,
        "search_pullrequests",
        "Semantic search over pull requests — titles, descriptions, reviewers and branch context.",
    )
    _make_search_tool(
        CollectionNames.TEST_RESULTS,
        "search_test_results",
        "Semantic search over test execution results — outcomes, error messages and stack traces.",
    )
    _make_search_tool(
        CollectionNames.COMMITS,
        "search_commits",
        "Semantic search over git commit history — messages, authors and change summaries.",
    )

    # ── Experience tools ───────────────────────────────────────────────────────

    @mcp.tool(
        name="retrieve_experience",
        description=(
            "ALWAYS call this tool at the START of every new task, conversation, or user request. "
            "It recalls relevant past experience: guidance on how to handle similar situations, "
            "known mistakes and their fixes, user preferences, and decisions from previous sessions. "
            "Returns guidance strings that should be followed for the current task. "
            "Pass a query describing the current situation or task."
        ),
    )
    async def retrieve_experience(query: str, top_k: int = 5) -> str:
        results = await memory_service.retrieve(query, top_k)
        if not results:
            return json.dumps({"experience": [], "note": "No relevant experience found."})
        return json.dumps({"experience": results}, default=str)

    @mcp.tool(
        name="remember",
        description=(
            "ALWAYS use this tool to store any information worth retaining across sessions. "
            "situation: describe the trigger — what kind of task, prompt, or context should surface this rule. "
            "guidance: the exact instruction to follow — what to do, avoid, or apply in that situation. "
            "Call this proactively whenever you learn something the user would want enforced in future conversations."
        ),
    )
    async def remember(situation: str, guidance: str) -> str:
        entry_id = await memory_service.remember(situation, guidance)
        return json.dumps({"status": "stored", "entry_id": entry_id})
