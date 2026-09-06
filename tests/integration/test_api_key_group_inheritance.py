from __future__ import annotations

import pytest
from sqlalchemy import select

from app.core.utils.time import utcnow
from app.db.models import Account, AccountStatus, ApiKeyLimit
from app.db.session import SessionLocal
from app.modules.api_keys.repository import ApiKeysRepository
from app.modules.api_keys.service import (
    ApiKeyRateLimitExceededError,
    ApiKeyRequestUsageBudget,
    ApiKeysService,
)

pytestmark = pytest.mark.integration


async def _add_account(account_id: str) -> None:
    now = utcnow()
    async with SessionLocal() as session:
        session.add(
            Account(
                id=account_id,
                email=f"{account_id}@example.com",
                plan_type="plus",
                access_token_encrypted=b"access",
                refresh_token_encrypted=b"refresh",
                id_token_encrypted=b"id",
                last_refresh=now,
                status=AccountStatus.ACTIVE,
            )
        )
        await session.commit()


async def _create_group(async_client, *, account_ids: list[str], max_value: int = 100) -> dict[str, object]:
    response = await async_client.post(
        "/api/account-groups/",
        json={
            "name": "Grouped keys",
            "accountIds": account_ids,
            "limits": [
                {
                    "limitType": "total_tokens",
                    "limitWindow": "weekly",
                    "maxValue": max_value,
                    "modelFilter": None,
                }
            ],
        },
    )
    assert response.status_code == 200
    return response.json()


@pytest.mark.asyncio
async def test_grouped_key_api_inherits_policy_rejects_overrides_and_detaches_safely(async_client, db_setup) -> None:
    del db_setup
    unknown_group = await async_client.post(
        "/api/api-keys/",
        json={"name": "unknown-group", "groupId": "missing"},
    )
    assert unknown_group.status_code == 400

    await _add_account("group-account")
    group = await _create_group(async_client, account_ids=["group-account"])

    created_response = await async_client.post(
        "/api/api-keys/",
        json={"name": "grouped", "groupId": group["id"]},
    )
    assert created_response.status_code == 200
    created = created_response.json()
    assert created["groupId"] == group["id"]
    assert created["accountAssignmentScopeEnabled"] is True
    assert created["assignedAccountIds"] == ["group-account"]
    assert created["limits"][0]["maxValue"] == 100

    metadata_update = await async_client.patch(
        f"/api/api-keys/{created['id']}",
        json={"name": "grouped-renamed"},
    )
    assert metadata_update.status_code == 200
    assert metadata_update.json()["groupId"] == group["id"]

    rejected = await async_client.patch(
        f"/api/api-keys/{created['id']}",
        json={"name": "must-not-stick", "assignedAccountIds": []},
    )
    assert rejected.status_code == 400

    regenerated = await async_client.post(f"/api/api-keys/{created['id']}/regenerate")
    assert regenerated.status_code == 200
    assert regenerated.json()["groupId"] == group["id"]
    assert regenerated.json()["assignedAccountIds"] == ["group-account"]

    async with SessionLocal() as session:
        await ApiKeysService(ApiKeysRepository(session)).record_usage(
            created["id"],
            model="model-alpha",
            input_tokens=20,
            output_tokens=5,
        )

    detached = await async_client.patch(
        f"/api/api-keys/{created['id']}",
        json={"groupId": None},
    )
    assert detached.status_code == 200
    detached_body = detached.json()
    assert detached_body["name"] == "grouped-renamed"
    assert detached_body["groupId"] is None
    assert detached_body["accountAssignmentScopeEnabled"] is True
    assert detached_body["assignedAccountIds"] == ["group-account"]
    assert detached_body["limits"][0]["currentValue"] == 25

    legacy = await async_client.post(
        "/api/api-keys/",
        json={"name": "legacy", "weeklyTokenLimit": 500},
    )
    assert legacy.status_code == 200
    assert legacy.json()["groupId"] is None
    assert legacy.json()["limits"][0]["maxValue"] == 500

    personal = (
        await async_client.post(
            "/api/api-keys/",
            json={
                "name": "personal-before-group",
                "limits": [
                    {"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 500},
                    {"limitType": "input_tokens", "limitWindow": "daily", "maxValue": 200},
                ],
            },
        )
    ).json()
    personal_total_id = next(limit["id"] for limit in personal["limits"] if limit["limitType"] == "total_tokens")
    async with SessionLocal() as session:
        await ApiKeysService(ApiKeysRepository(session)).record_usage(
            personal["id"],
            model="model-alpha",
            input_tokens=10,
            output_tokens=5,
        )

    attached = await async_client.patch(
        f"/api/api-keys/{personal['id']}",
        json={"groupId": group["id"]},
    )
    assert attached.status_code == 200
    attached_body = attached.json()
    assert attached_body["groupId"] == group["id"]
    assert len(attached_body["limits"]) == 1
    assert (
        attached_body["limits"][0]["id"],
        attached_body["limits"][0]["limitType"],
        attached_body["limits"][0]["maxValue"],
        attached_body["limits"][0]["currentValue"],
    ) == (personal_total_id, "total_tokens", 100, 15)


@pytest.mark.asyncio
async def test_group_amount_edit_preserves_reservation_and_independent_key_counters(async_client, db_setup) -> None:
    del db_setup
    group = await _create_group(async_client, account_ids=[], max_value=100)
    first_response = await async_client.post("/api/api-keys/", json={"name": "first", "groupId": group["id"]})
    second_response = await async_client.post("/api/api-keys/", json={"name": "second", "groupId": group["id"]})
    assert first_response.status_code == second_response.status_code == 200
    first = first_response.json()
    second = second_response.json()
    assert first["accountAssignmentScopeEnabled"] is second["accountAssignmentScopeEnabled"] is True
    assert first["assignedAccountIds"] == second["assignedAccountIds"] == []

    async with SessionLocal() as session:
        service = ApiKeysService(ApiKeysRepository(session))
        reservation = await service.enforce_limits_for_request(
            first["id"],
            request_model="model-alpha",
            request_usage_budget=ApiKeyRequestUsageBudget(input_tokens=40, output_tokens=0),
        )
        assert reservation is not None
        original = (await ApiKeysRepository(session).get_limits_by_key(first["id"]))[0]
        original_state = (original.id, original.current_value, original.reset_at)

    updated_group = await async_client.put(
        f"/api/account-groups/{group['id']}",
        json={
            "name": "Grouped keys",
            "accountIds": [],
            "limits": [
                {
                    "limitType": "total_tokens",
                    "limitWindow": "weekly",
                    "maxValue": 20,
                    "modelFilter": None,
                }
            ],
        },
    )
    assert updated_group.status_code == 200

    async with SessionLocal() as session:
        limits = (
            (
                await session.execute(
                    select(ApiKeyLimit)
                    .where(ApiKeyLimit.api_key_id.in_([first["id"], second["id"]]))
                    .order_by(ApiKeyLimit.api_key_id)
                )
            )
            .scalars()
            .all()
        )
        limits_by_key = {limit.api_key_id: limit for limit in limits}
        first_limit = limits_by_key[first["id"]]
        assert (first_limit.id, first_limit.current_value, first_limit.reset_at) == original_state
        assert first_limit.max_value == 20
        assert limits_by_key[second["id"]].current_value == 0

        service = ApiKeysService(ApiKeysRepository(session))
        await service.finalize_usage_reservation(
            reservation.reservation_id,
            model="model-alpha",
            input_tokens=25,
            output_tokens=0,
        )
        await service.finalize_usage_reservation(
            reservation.reservation_id,
            model="model-alpha",
            input_tokens=25,
            output_tokens=0,
        )

    async with SessionLocal() as session:
        repository = ApiKeysRepository(session)
        assert (await repository.get_limits_by_key(first["id"]))[0].current_value == 25
        assert (await repository.get_limits_by_key(second["id"]))[0].current_value == 0
        with pytest.raises(ApiKeyRateLimitExceededError):
            await ApiKeysService(repository).enforce_limits_for_request(
                first["id"],
                request_model="model-alpha",
                request_usage_budget=ApiKeyRequestUsageBudget(input_tokens=1, output_tokens=0),
            )

    detached_empty = await async_client.patch(
        f"/api/api-keys/{second['id']}",
        json={"groupId": None},
    )
    assert detached_empty.status_code == 200
    assert detached_empty.json()["accountAssignmentScopeEnabled"] is True
    assert detached_empty.json()["assignedAccountIds"] == []
    assert detached_empty.json()["limits"][0]["maxValue"] == 20
