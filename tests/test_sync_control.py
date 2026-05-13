import asyncio

import pytest

from app.store.sync_control import SyncCancelled, SyncControlStore


# ── is_paused ──────────────────────────────────────────────────────────────────

def test_is_paused_false_for_unknown_source():
    assert not SyncControlStore().is_paused("x")


def test_is_paused_false_after_register():
    store = SyncControlStore()
    store.register("s1")
    assert not store.is_paused("s1")


def test_is_paused_true_after_pause():
    store = SyncControlStore()
    store.register("s1")
    store.pause("s1")
    assert store.is_paused("s1")


def test_is_paused_false_after_resume():
    store = SyncControlStore()
    store.register("s1")
    store.pause("s1")
    store.resume("s1")
    assert not store.is_paused("s1")


# ── request_cancel wakes paused event ─────────────────────────────────────────

def test_request_cancel_wakes_paused_event():
    store = SyncControlStore()
    store.register("s1")
    store.pause("s1")
    store.request_cancel("s1")
    assert not store.is_paused("s1")


# ── register resets state ──────────────────────────────────────────────────────

async def test_register_clears_cancel_flag():
    store = SyncControlStore()
    store.register("s1")
    store.request_cancel("s1")
    store.register("s1")
    await store.checkpoint("s1")  # should not raise


def test_register_clears_pause():
    store = SyncControlStore()
    store.register("s1")
    store.pause("s1")
    store.register("s1")
    assert not store.is_paused("s1")


# ── checkpoint: pass / raise ───────────────────────────────────────────────────

async def test_checkpoint_passes_when_running():
    store = SyncControlStore()
    store.register("s1")
    await store.checkpoint("s1")  # no exception


async def test_checkpoint_raises_sync_cancelled():
    store = SyncControlStore()
    store.register("s1")
    store.request_cancel("s1")
    with pytest.raises(SyncCancelled):
        await store.checkpoint("s1")


async def test_checkpoint_waits_while_paused_then_raises_on_cancel():
    store = SyncControlStore()
    store.register("s1")
    store.pause("s1")

    async def cancel_after_brief_delay():
        await asyncio.sleep(0.02)
        store.request_cancel("s1")

    asyncio.create_task(cancel_after_brief_delay())
    with pytest.raises(SyncCancelled):
        await store.checkpoint("s1")


async def test_checkpoint_resumes_without_cancel():
    store = SyncControlStore()
    store.register("s1")
    store.pause("s1")

    async def resume_after_brief_delay():
        await asyncio.sleep(0.02)
        store.resume("s1")

    asyncio.create_task(resume_after_brief_delay())
    await store.checkpoint("s1")  # should not raise after resume


# ── clear ─────────────────────────────────────────────────────────────────────

async def test_clear_removes_cancel_flag():
    store = SyncControlStore()
    store.register("s1")
    store.request_cancel("s1")
    store.clear("s1")
    await store.checkpoint("s1")  # no exception (no cancel flag)


def test_clear_removes_pause():
    store = SyncControlStore()
    store.register("s1")
    store.pause("s1")
    store.clear("s1")
    assert not store.is_paused("s1")


def test_clear_is_idempotent():
    store = SyncControlStore()
    store.register("s1")
    store.clear("s1")
    store.clear("s1")  # second call must not raise


# ── multiple sources are independent ──────────────────────────────────────────

async def test_cancel_one_source_does_not_affect_another():
    store = SyncControlStore()
    store.register("s1")
    store.register("s2")
    store.request_cancel("s1")

    with pytest.raises(SyncCancelled):
        await store.checkpoint("s1")
    await store.checkpoint("s2")  # s2 should still pass


def test_pause_one_source_does_not_affect_another():
    store = SyncControlStore()
    store.register("s1")
    store.register("s2")
    store.pause("s1")
    assert store.is_paused("s1")
    assert not store.is_paused("s2")
