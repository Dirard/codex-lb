from __future__ import annotations

import asyncio
import json
from collections import deque
from contextlib import nullcontext
from types import SimpleNamespace
from typing import Any, cast
from unittest.mock import AsyncMock

import anyio
import pytest

import app.modules.proxy._service.support as proxy_support_module
import app.modules.proxy.service as proxy_service
from app.core.clients.proxy_websocket import UpstreamWebSocket
from app.core.config.settings import Settings
from app.db.models import AccountStatus
from app.modules.api_keys.service import ApiKeyUsageReservationData

pytestmark = pytest.mark.unit

FRESH_TEXT = (
    '{"type":"response.create","model":"gpt-5.6-sol",'
    '"input":[{"role":"user","content":[{"type":"input_text","text":"full resend"}]}]}'
)


def _account(account_id: str) -> Any:
    return SimpleNamespace(id=account_id, status=AccountStatus.ACTIVE, plan_type="plus")


def _error_event(code: str, message: str, *, status: int = 429) -> str:
    return json.dumps(
        {"type": "error", "status": status, "error": {"type": code, "code": code, "message": message}},
        separators=(",", ":"),
    )


def _request_state(request_id: str, *, owner_account_id: str | None = None) -> proxy_service._WebSocketRequestState:
    return proxy_service._WebSocketRequestState(
        request_id=request_id,
        model="gpt-5.6-sol",
        service_tier="priority" if owner_account_id else None,
        reasoning_effort="high" if owner_account_id else None,
        api_key_reservation=ApiKeyUsageReservationData(
            reservation_id=f"resv-{request_id}",
            key_id=f"key-{request_id}",
            model="gpt-5.6-sol",
        ),
        started_at=1.0,
        previous_response_id="resp-quota-owner" if owner_account_id else None,
        preferred_account_id=owner_account_id,
        proxy_injected_previous_response_id=owner_account_id is not None,
        fresh_upstream_request_text=FRESH_TEXT if owner_account_id else None,
        fresh_upstream_request_is_retry_safe=owner_account_id is not None,
        hard_continuity_anchor=owner_account_id is not None,
        awaiting_response_created=True,
        event_queue=asyncio.Queue(),
        request_text=(
            '{"type":"response.create","model":"gpt-5.6-sol",'
            '"previous_response_id":"resp-quota-owner","input":"trimmed"}'
            if owner_account_id
            else '{"type":"response.create","model":"gpt-5.6-sol","input":"retry"}'
        ),
        transport="http",
        skip_request_log=True,
    )


def _record_settlement_and_health(
    monkeypatch: pytest.MonkeyPatch,
    service: proxy_service.ProxyService,
) -> tuple[list[dict[str, Any]], list[str]]:
    settle_calls: list[dict[str, Any]] = []
    health_calls: list[str] = []

    async def record_settle(*args: object, **kwargs: Any) -> bool:
        del args
        settle_calls.append(kwargs)
        return True

    async def record_health(account: object, error: object, code: str) -> None:
        del error
        assert settle_calls, "account health must follow confirmed settlement"
        health_calls.append(f"{cast(Any, account).id}:{code}")

    monkeypatch.setattr(service, "_settle_stream_api_key_usage", record_settle)
    monkeypatch.setattr(service, "_handle_stream_error", record_health)
    return settle_calls, health_calls


def _session(
    request_state: proxy_service._WebSocketRequestState,
    account: Any,
    upstream: UpstreamWebSocket,
) -> proxy_service._HTTPBridgeSession:
    return proxy_service._HTTPBridgeSession(
        key=proxy_service._HTTPBridgeSessionKey("thread_header", request_state.request_id, None),
        headers={"x-codex-turn-state": request_state.request_id},
        affinity=proxy_service._AffinityPolicy(
            key=request_state.request_id,
            kind=proxy_service.StickySessionKind.CODEX_SESSION,
        ),
        request_model="gpt-5.6-sol",
        account=account,
        upstream=upstream,
        upstream_control=proxy_service._WebSocketUpstreamControl(),
        pending_requests=deque([request_state]),
        pending_lock=anyio.Lock(),
        response_create_gate=asyncio.Semaphore(1),
        queued_request_count=1,
        last_used_at=1.0,
        idle_ttl_seconds=120.0,
    )


@pytest.mark.asyncio
@pytest.mark.parametrize("replacement_error", ["account_suspended", "usage_limit_reached"])
async def test_http_bridge_quota_retry_keeps_replacement_health_independent(
    monkeypatch: pytest.MonkeyPatch,
    replacement_error: str,
) -> None:
    service = proxy_service.ProxyService(cast(Any, nullcontext()))
    account_a = _account("acc-quota-a")
    account_b = _account("acc-quota-b")
    request_state = _request_state("req-quota-retry-health", owner_account_id=account_a.id)
    send_text = AsyncMock()
    replacement_upstream = cast(
        UpstreamWebSocket,
        SimpleNamespace(send_text=send_text, close=AsyncMock(), response_header=lambda _name: None),
    )
    session = _session(
        request_state,
        account_a,
        cast(UpstreamWebSocket, SimpleNamespace(close=AsyncMock())),
    )
    service._durable_bridge = cast(
        Any,
        SimpleNamespace(lookup_retry_circuit=AsyncMock(return_value=None)),
    )
    settle_calls, health_calls = _record_settlement_and_health(monkeypatch, service)

    async def select_account(_deadline: float, **_: object) -> proxy_service.AccountSelection:
        return proxy_service.AccountSelection(account=account_b, error_message=None)

    async def ensure_fresh(account: object, **_: object) -> object:
        return account

    async def open_upstream(_account: object, _headers: dict[str, str], **_: object) -> UpstreamWebSocket:
        return replacement_upstream

    settings = Settings(
        http_responses_session_bridge_enabled=True,
        http_responses_session_bridge_request_budget_seconds=30.0,
    )
    monkeypatch.setattr(proxy_service, "get_settings", lambda: settings)
    monkeypatch.setattr(
        proxy_service,
        "get_settings_cache",
        lambda: SimpleNamespace(
            get=AsyncMock(
                return_value=SimpleNamespace(
                    prefer_earlier_reset_accounts=False,
                    prefer_earlier_reset_window="secondary",
                    routing_strategy="usage_weighted",
                )
            )
        ),
    )
    monkeypatch.setattr(service, "_select_account_with_budget_for_stream", select_account)
    monkeypatch.setattr(service, "_ensure_fresh_with_budget", ensure_fresh)
    monkeypatch.setattr(service, "_open_upstream_websocket_with_budget", open_upstream)
    monkeypatch.setattr(service, "_acquire_account_response_create_lease_or_overload", AsyncMock())
    monkeypatch.setattr(service, "_release_request_state_account_response_create_lease", AsyncMock())
    monkeypatch.setattr(service._load_balancer, "release_account_lease", AsyncMock())

    await service._process_http_bridge_upstream_text(session, _error_event("usage_limit_reached", "usage limit"))

    assert send_text.await_count == 1
    assert session.account is account_b
    assert request_state.account_health_error_handled is False
    assert [penalty.account for penalty in request_state.deferred_keyed_stream_health] == [account_a]
    assert settle_calls == []
    assert health_calls == []

    await service._process_http_bridge_upstream_text(
        session,
        json.dumps({"type": "response.created", "response": {"id": "resp-replacement-accepted"}}),
    )

    await service._process_http_bridge_upstream_text(
        session,
        _error_event(
            replacement_error, "replacement rejected", status=403 if replacement_error == "account_suspended" else 429
        ),
    )

    assert len(settle_calls) == 1
    assert settle_calls[0]["wait_for_settlement"] is True
    assert health_calls == [
        "acc-quota-a:usage_limit_reached",
        f"acc-quota-b:{replacement_error}",
    ]
    assert request_state.deferred_keyed_stream_health == []


@pytest.mark.asyncio
async def test_http_bridge_cancelled_retry_send_restores_original_health_marker(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    from app.modules.proxy._service.http_bridge.request_submit import (
        _send_http_bridge_request_text_with_archive_id,
    )

    service = proxy_service.ProxyService(cast(Any, nullcontext()))
    account = _account("acc-cancelled-retry")
    request_state = _request_state("req-cancelled-retry-health")
    request_state.deferred_keyed_stream_health.append(
        proxy_support_module._DeferredKeyedStreamHealthPenalty(
            account=account,
            error={"message": "usage limit reached"},
            code="usage_limit_reached",
        )
    )
    request_state.account_health_error_handled = True
    upstream = cast(
        UpstreamWebSocket,
        SimpleNamespace(send_text=AsyncMock(side_effect=asyncio.CancelledError()), close=AsyncMock()),
    )
    settle_calls, health_calls = _record_settlement_and_health(monkeypatch, service)

    with pytest.raises(asyncio.CancelledError):
        await _send_http_bridge_request_text_with_archive_id(
            _session(request_state, account, upstream),
            request_state,
            request_state.request_text or "",
        )

    assert request_state.account_health_error_handled is True

    await service._finalize_websocket_request_state(
        request_state,
        account=account,
        account_id_value=account.id,
        event=None,
        event_type="error",
        payload={"type": "error", "error": {"code": "usage_limit_reached", "message": "usage limit"}},
        api_key=None,
        upstream_control=proxy_service._WebSocketUpstreamControl(),
        response_create_gate=asyncio.Semaphore(1),
    )

    assert len(settle_calls) == 1
    assert settle_calls[0]["wait_for_settlement"] is True
    assert health_calls == [f"{account.id}:usage_limit_reached"]
    assert request_state.deferred_keyed_stream_health == []
