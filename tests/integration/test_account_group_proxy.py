from __future__ import annotations

import json

import pytest

import app.modules.proxy.service as proxy_module
from tests.integration.test_api_keys_api import _TEST_MODELS, _import_account, _populate_test_registry

pytestmark = pytest.mark.integration


@pytest.mark.asyncio
@pytest.mark.parametrize("path", ["/backend-api/codex/responses", "/v1/responses"])
async def test_group_membership_changes_reach_cached_http_keys(async_client, monkeypatch, path):
    await _populate_test_registry()
    model = sorted(_TEST_MODELS)[0]
    enabled = await async_client.put(
        "/api/settings",
        json={
            "stickyThreadsEnabled": False,
            "preferEarlierResetAccounts": False,
            "totpRequiredOnLogin": False,
            "apiKeyAuthEnabled": True,
        },
    )
    assert enabled.status_code == 200
    account_a = await _import_account(async_client, "upstream-a", "group-a@example.test")
    account_b = await _import_account(async_client, "upstream-b", "group-b@example.test")
    group_body = {"name": "Shared policy", "accountIds": [account_a], "limits": []}
    created_group = await async_client.post("/api/account-groups/", json=group_body)
    assert created_group.status_code == 200
    group_id = created_group.json()["id"]
    keys = []
    for name in ("Key A", "Key B"):
        created = await async_client.post("/api/api-keys/", json={"name": name, "groupId": group_id})
        assert created.status_code == 200
        keys.append(created.json()["key"])

    selected_upstreams = []

    async def fake_stream(payload, _headers, _access_token, _account_id, **_kwargs):
        selected_upstreams.append(_account_id)
        event = {
            "type": "response.completed",
            "response": {
                "id": f"resp_{len(selected_upstreams)}",
                "usage": {"input_tokens": 3, "output_tokens": 2},
            },
        }
        yield f"data: {json.dumps(event)}\n\n"

    monkeypatch.setattr(proxy_module, "core_stream_responses", fake_stream)
    for members in ([account_a], [account_b]):
        updated = await async_client.put(
            f"/api/account-groups/{group_id}", json={**group_body, "accountIds": members}
        )
        assert updated.status_code == 200
        for key in keys:
            response = await async_client.post(
                path,
                headers={"Authorization": f"Bearer {key}"},
                json={"model": model, "input": [], "stream": True},
            )
            assert response.status_code == 200
            assert "response.completed" in response.text

    assert selected_upstreams == ["upstream-a", "upstream-a", "upstream-b", "upstream-b"]
    emptied = await async_client.put(f"/api/account-groups/{group_id}", json={**group_body, "accountIds": []})
    assert emptied.status_code == 200
    response = await async_client.post(
        path,
        headers={"Authorization": f"Bearer {keys[0]}"},
        json={"model": model, "input": [], "stream": True},
    )
    assert "response.completed" not in response.text
    assert "error" in response.text
    assert selected_upstreams == ["upstream-a", "upstream-a", "upstream-b", "upstream-b"]
