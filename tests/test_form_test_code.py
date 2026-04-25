"""
Tests for _build_source_from_form – Test Code source type.

Mirrors test_form_code_repo.py. The "testcode" type shares the same three
config fields (Repository, Branch, GlobPatterns) with a different server default
for GlobPatterns.
"""
from app.models import ConfigKeys, SourceTypes
from form_helpers import _cfg


def _tcode(**fields) -> dict:
    return _cfg(SourceTypes.TEST_CODE_REPO, **fields)


# ── Core fields ───────────────────────────────────────────────────────────────

def test_testcode_repository_stored():
    cfg = _tcode(ConfigRepository="MyRepo", ConfigBranch="Main")
    assert cfg[ConfigKeys.REPOSITORY] == "MyRepo"


def test_testcode_branch_stored():
    cfg = _tcode(ConfigRepository="MyRepo", ConfigBranch="develop")
    assert cfg[ConfigKeys.BRANCH] == "develop"


def test_testcode_branch_default_main_from_create_template():
    cfg = _tcode(ConfigRepository="MyRepo", ConfigBranch="Main")
    assert cfg[ConfigKeys.BRANCH] == "Main"


def test_testcode_branch_absent_when_field_empty():
    cfg = _tcode(ConfigRepository="MyRepo", ConfigBranch="")
    assert ConfigKeys.BRANCH not in cfg


def test_testcode_branch_absent_when_not_submitted():
    cfg = _tcode(ConfigRepository="MyRepo")
    assert ConfigKeys.BRANCH not in cfg


# ── Glob patterns ─────────────────────────────────────────────────────────────

def test_testcode_glob_server_default_when_absent():
    cfg = _tcode(ConfigRepository="R", ConfigBranch="Main")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*Tests.cs,**/*Spec.cs,**/*test*.py,**/*.spec.ts,**/*.test.ts"


def test_testcode_glob_custom_pattern():
    cfg = _tcode(ConfigRepository="R", ConfigBranch="Main", ConfigGlobPatterns="**/*Tests.cs")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*Tests.cs"


def test_testcode_glob_multiple_patterns():
    cfg = _tcode(ConfigRepository="R", ConfigBranch="Main",
                 ConfigGlobPatterns="**/*Tests.cs,**/*Spec.cs")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*Tests.cs,**/*Spec.cs"


def test_testcode_glob_python_test_pattern():
    cfg = _tcode(ConfigRepository="R", ConfigBranch="Main", ConfigGlobPatterns="**/*test*.py")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*test*.py"


def test_testcode_glob_typescript_spec_pattern():
    cfg = _tcode(ConfigRepository="R", ConfigBranch="Main", ConfigGlobPatterns="**/*.spec.ts,**/*.test.ts")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.spec.ts,**/*.test.ts"


# ── ADO connection card ────────────────────────────────────────────────────────

def test_testcode_ado_base_url_stored():
    cfg = _tcode(ConfigRepository="R", ConfigBranch="Main",
                 ConfigBaseUrl="https://dev.azure.com/myorg/myproject")
    assert cfg[ConfigKeys.BASE_URL] == "https://dev.azure.com/myorg/myproject"


def test_testcode_auth_pat_stores_pat_only():
    cfg = _tcode(ConfigRepository="R", ConfigBranch="Main",
                 ConfigAuthType="pat",
                 ConfigPat="MY_PAT",
                 ConfigToken="SHOULD_BE_IGNORED")
    assert cfg[ConfigKeys.PAT] == "MY_PAT"
    assert ConfigKeys.TOKEN not in cfg


def test_testcode_auth_bearer_stores_token_only():
    cfg = _tcode(ConfigRepository="R", ConfigBranch="Main",
                 ConfigAuthType="bearer",
                 ConfigPat="SHOULD_BE_IGNORED",
                 ConfigToken="MY_TOKEN")
    assert cfg[ConfigKeys.TOKEN] == "MY_TOKEN"
    assert ConfigKeys.PAT not in cfg
