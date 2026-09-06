from __future__ import annotations

import pytest
from sqlalchemy import select, update

from app.core.auth.dependencies import require_dashboard_write_access
from app.core.crypto import TokenEncryptor
from app.core.exceptions import DashboardPermissionError
from app.core.utils.time import utcnow
from app.db.models import Account, AccountStatus, ApiKey, ApiKeyLimit, LimitType, LimitWindow
from app.db.session import SessionLocal
from app.modules.api_keys.limit_windows import next_limit_reset

pytestmark = pytest.mark.integration


def _make_account(account_id: str) -> Account:
    encryptor = TokenEncryptor()
    return Account(
        id=account_id,
        email=f"{account_id}@example.com",
        plan_type="plus",
        access_token_encrypted=encryptor.encrypt("access"),
        refresh_token_encrypted=encryptor.encrypt("refresh"),
        id_token_encrypted=encryptor.encrypt("id"),
        last_refresh=utcnow(),
        status=AccountStatus.ACTIVE,
        deactivation_reason=None,
    )


async def _add_accounts(account_ids: list[str]) -> None:
    async with SessionLocal() as session:
        session.add_all([_make_account(account_id) for account_id in account_ids])
        await session.commit()


async def _create_group(async_client, *, name: str, account_ids: list[str] | None = None, limits: list | None = None):
    return await async_client.post(
        "/api/account-groups/",
        json={"name": name, "accountIds": account_ids or [], "limits": limits or []},
    )


async def _create_linked_keys(async_client, group_id: str, count: int) -> list[str]:
    key_ids: list[str] = []
    for index in range(count):
        created = await async_client.post("/api/api-keys/", json={"name": f"group-key-{index}"})
        assert created.status_code == 200
        key_ids.append(created.json()["id"])
    async with SessionLocal() as session:
        await session.execute(update(ApiKey).where(ApiKey.id.in_(key_ids)).values(group_id=group_id))
        await session.commit()
    return key_ids


async def _seed_key_limit(
    key_id: str,
    *,
    max_value: int = 1000,
    current_value: int = 300,
) -> None:
    async with SessionLocal() as session:
        session.add(
            ApiKeyLimit(
                api_key_id=key_id,
                limit_type=LimitType.TOTAL_TOKENS,
                limit_window=LimitWindow.WEEKLY,
                max_value=max_value,
                current_value=current_value,
                model_filter=None,
                reset_at=next_limit_reset(utcnow(), LimitWindow.WEEKLY),
            )
        )
        await session.commit()


async def _key_limit_values(key_id: str) -> dict[tuple[str, str, str | None], tuple[int, int]]:
    async with SessionLocal() as session:
        result = await session.execute(select(ApiKeyLimit).where(ApiKeyLimit.api_key_id == key_id))
        return {
            (limit.limit_type.value, limit.limit_window.value, limit.model_filter): (
                limit.max_value,
                limit.current_value,
            )
            for limit in result.scalars().all()
        }


@pytest.mark.asyncio
async def test_account_group_crud_round_trip(async_client):
    await _add_accounts(["acc-a", "acc-b"])

    created = await _create_group(
        async_client,
        name="  Team  ",
        account_ids=["acc-a"],
        limits=[{"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 1000}],
    )
    assert created.status_code == 200
    payload = created.json()
    group_id = payload["id"]
    assert payload["name"] == "Team"
    assert payload["accountIds"] == ["acc-a"]
    assert payload["keyCount"] == 0
    assert payload["limits"] == [
        {"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 1000, "modelFilter": None}
    ]

    updated = await async_client.put(
        f"/api/account-groups/{group_id}",
        json={
            "name": "Team 2",
            "accountIds": ["acc-b"],
            "limits": [
                {"limitType": "credits", "limitWindow": "monthly", "maxValue": 500},
                {"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 2000},
            ],
        },
    )
    assert updated.status_code == 200
    assert updated.json() == {
        "id": group_id,
        "name": "Team 2",
        "accountIds": ["acc-b"],
        "keyCount": 0,
        "limits": [
            {"limitType": "credits", "limitWindow": "monthly", "maxValue": 500, "modelFilter": None},
            {"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 2000, "modelFilter": None},
        ],
    }

    listed = await async_client.get("/api/account-groups/")
    assert listed.status_code == 200
    assert listed.json() == [updated.json()]

    deleted = await async_client.delete(f"/api/account-groups/{group_id}")
    assert deleted.status_code == 204
    assert (await async_client.get("/api/account-groups/")).json() == []


@pytest.mark.asyncio
async def test_account_group_rejects_invalid_accounts_and_limits_without_partial_write(async_client):
    await _add_accounts(["acc-a"])
    created = await _create_group(async_client, name="Team", account_ids=["acc-a"])
    assert created.status_code == 200
    group = created.json()

    unknown_account = await async_client.put(
        f"/api/account-groups/{group['id']}",
        json={"name": "Changed", "accountIds": ["missing"], "limits": []},
    )
    assert unknown_account.status_code == 400
    assert unknown_account.json()["error"]["code"] == "invalid_account_group_payload"

    duplicate_limits = await async_client.put(
        f"/api/account-groups/{group['id']}",
        json={
            "name": "Team",
            "accountIds": ["acc-a"],
            "limits": [
                {"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 100},
                {"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 200},
            ],
        },
    )
    assert duplicate_limits.status_code == 400

    credits_with_model_filter = await async_client.put(
        f"/api/account-groups/{group['id']}",
        json={
            "name": "Team",
            "accountIds": ["acc-a"],
            "limits": [
                {"limitType": "credits", "limitWindow": "weekly", "maxValue": 100, "modelFilter": "gpt-5"}
            ],
        },
    )
    assert credits_with_model_filter.status_code == 400

    schema_invalid = await async_client.put(
        f"/api/account-groups/{group['id']}",
        json={
            "name": "Team",
            "accountIds": ["acc-a"],
            "limits": [{"limitType": "total_tokens", "limitWindow": "yearly", "maxValue": 100}],
        },
    )
    assert schema_invalid.status_code == 422

    listed = await async_client.get("/api/account-groups/")
    assert listed.json() == [group]


@pytest.mark.asyncio
async def test_account_group_membership_and_name_conflicts_return_409(async_client):
    await _add_accounts(["acc-a", "acc-b"])
    first = (await _create_group(async_client, name="First", account_ids=["acc-a"])).json()
    second = (await _create_group(async_client, name="Second", account_ids=["acc-b"])).json()

    membership_conflict = await async_client.put(
        f"/api/account-groups/{second['id']}",
        json={"name": "Second", "accountIds": ["acc-a", "acc-b"], "limits": []},
    )
    assert membership_conflict.status_code == 409
    assert membership_conflict.json()["error"]["code"] == "conflict"

    name_conflict = await _create_group(async_client, name="First", account_ids=[])
    assert name_conflict.status_code == 409

    listed = await async_client.get("/api/account-groups/")
    assert listed.json() == [first, second]


@pytest.mark.asyncio
async def test_account_group_create_rejects_account_in_another_group(async_client):
    await _add_accounts(["acc-a"])
    first = await _create_group(async_client, name="First", account_ids=["acc-a"])
    assert first.status_code == 200

    conflict = await _create_group(async_client, name="Second", account_ids=["acc-a"])
    assert conflict.status_code == 409
    assert (await async_client.get("/api/account-groups/")).json() == [first.json()]


@pytest.mark.asyncio
async def test_account_group_delete_conflicts_while_keys_are_linked(async_client):
    await _add_accounts(["acc-a"])
    group = (await _create_group(async_client, name="Team", account_ids=["acc-a"])).json()
    await _create_linked_keys(async_client, group["id"], 1)

    deleted = await async_client.delete(f"/api/account-groups/{group['id']}")
    assert deleted.status_code == 409
    assert (await async_client.get("/api/account-groups/")).json()[0]["keyCount"] == 1


@pytest.mark.asyncio
async def test_account_group_unknown_group_returns_404(async_client):
    update_response = await async_client.put(
        "/api/account-groups/ag_missing",
        json={"name": "Team", "accountIds": [], "limits": []},
    )
    delete_response = await async_client.delete("/api/account-groups/ag_missing")
    assert update_response.status_code == 404
    assert delete_response.status_code == 404


@pytest.mark.asyncio
async def test_account_group_mutations_require_dashboard_write_access(async_client, app_instance):
    group = (await _create_group(async_client, name="Team")).json()

    async def reject_write_access() -> None:
        raise DashboardPermissionError(
            "Read-only dashboard access cannot modify dashboard state",
            code="read_only_access",
        )

    app_instance.dependency_overrides[require_dashboard_write_access] = reject_write_access
    try:
        responses = [
            await _create_group(async_client, name="Denied"),
            await async_client.put(
                f"/api/account-groups/{group['id']}",
                json={"name": "Denied", "accountIds": [], "limits": []},
            ),
            await async_client.delete(f"/api/account-groups/{group['id']}"),
        ]
    finally:
        app_instance.dependency_overrides.pop(require_dashboard_write_access, None)

    assert [(response.status_code, response.json()["error"]["code"]) for response in responses] == [
        (403, "read_only_access"),
        (403, "read_only_access"),
        (403, "read_only_access"),
    ]
    assert (await async_client.get("/api/account-groups/")).json() == [group]


@pytest.mark.asyncio
async def test_account_group_update_syncs_linked_key_limits_preserving_usage(async_client):
    await _add_accounts(["acc-a"])
    group = (
        await _create_group(
            async_client,
            name="Team",
            account_ids=["acc-a"],
            limits=[{"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 1000}],
        )
    ).json()
    key_ids = await _create_linked_keys(async_client, group["id"], 2)
    await _seed_key_limit(key_ids[0], max_value=1000, current_value=300)

    updated = await async_client.put(
        f"/api/account-groups/{group['id']}",
        json={
            "name": "Team",
            "accountIds": ["acc-a"],
            "limits": [
                {"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 2000},
                {"limitType": "credits", "limitWindow": "monthly", "maxValue": 500},
            ],
        },
    )
    assert updated.status_code == 200
    assert updated.json()["keyCount"] == 2

    assert await _key_limit_values(key_ids[0]) == {
        ("total_tokens", "weekly", None): (2000, 300),
        ("credits", "monthly", None): (500, 0),
    }
    assert await _key_limit_values(key_ids[1]) == {
        ("total_tokens", "weekly", None): (2000, 0),
        ("credits", "monthly", None): (500, 0),
    }
