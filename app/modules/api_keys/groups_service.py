from __future__ import annotations

from dataclasses import dataclass, field
from uuid import uuid4

from sqlalchemy.exc import IntegrityError

from app.core.auth.api_key_cache import get_api_key_cache
from app.core.cache.invalidation import NAMESPACE_API_KEY, get_cache_invalidation_poller
from app.core.utils.time import utcnow
from app.db.models import AccountGroup, AccountGroupAccount, AccountGroupLimit, LimitType, LimitWindow
from app.db.session import sqlite_writer_section
from app.modules.api_keys.groups_repository import AccountGroupsRepository
from app.modules.api_keys.service import (
    ApiKeysService,
    ApiKeyValidationError,
    LimitRuleInput,
    _validate_unique_limit_rule_identities,
)


class AccountGroupNotFoundError(ValueError):
    pass


class AccountGroupValidationError(ValueError):
    pass


class AccountGroupConflictError(ValueError):
    pass


@dataclass(frozen=True, slots=True)
class AccountGroupData:
    id: str
    name: str
    account_ids: list[str] = field(default_factory=list)
    limits: list[LimitRuleInput] = field(default_factory=list)
    key_count: int = 0


def _normalize_name(name: str) -> str:
    normalized = name.strip()
    if not normalized:
        raise AccountGroupValidationError("Account group name is required")
    return normalized


def _normalize_account_ids(account_ids: list[str]) -> list[str]:
    normalized: list[str] = []
    seen: set[str] = set()
    for account_id in account_ids:
        value = account_id.strip()
        if not value or value in seen:
            continue
        normalized.append(value)
        seen.add(value)
    return normalized


def _validate_limits(limits: list[LimitRuleInput]) -> None:
    try:
        _validate_unique_limit_rule_identities(limits)
    except ApiKeyValidationError as exc:
        raise AccountGroupValidationError(str(exc)) from exc
    for limit in limits:
        # Same rule as ApiKeysService._limit_input_to_row.
        if limit.limit_type == LimitType.CREDITS.value and limit.model_filter is not None:
            raise AccountGroupValidationError("credits limits do not support model_filter")


def _sorted_limits(limits: list[LimitRuleInput]) -> list[LimitRuleInput]:
    return sorted(limits, key=lambda limit: (limit.limit_type, limit.limit_window, limit.model_filter or ""))


def _limit_rows(group_id: str, limits: list[LimitRuleInput]) -> list[AccountGroupLimit]:
    return [
        AccountGroupLimit(
            group_id=group_id,
            limit_type=LimitType(limit.limit_type),
            limit_window=LimitWindow(limit.limit_window),
            max_value=limit.max_value,
            model_filter=limit.model_filter,
        )
        for limit in limits
    ]


def _to_data(group: AccountGroup, *, key_count: int) -> AccountGroupData:
    return AccountGroupData(
        id=group.id,
        name=group.name,
        account_ids=sorted(assignment.account_id for assignment in group.account_assignments),
        limits=[
            LimitRuleInput(
                limit_type=limit.limit_type.value,
                limit_window=limit.limit_window.value,
                max_value=limit.max_value,
                model_filter=limit.model_filter,
            )
            for limit in sorted(
                group.limits,
                key=lambda limit: (limit.limit_type.value, limit.limit_window.value, limit.model_filter or ""),
            )
        ],
        key_count=key_count,
    )


class AccountGroupsService:
    def __init__(self, repository: AccountGroupsRepository, api_keys_service: ApiKeysService) -> None:
        self._repository = repository
        self._api_keys_service = api_keys_service

    async def list_groups(self) -> list[AccountGroupData]:
        groups = await self._repository.list_all()
        key_counts = await self._repository.key_counts_by_group()
        return [_to_data(group, key_count=key_counts.get(group.id, 0)) for group in groups]

    async def create_group(
        self, *, name: str, account_ids: list[str], limits: list[LimitRuleInput]
    ) -> AccountGroupData:
        normalized_name = _normalize_name(name)
        normalized_account_ids = _normalize_account_ids(account_ids)
        _validate_limits(limits)
        group_id = f"ag_{uuid4().hex}"

        async with sqlite_writer_section():
            try:
                await self._validate_mutation(group_id=None, name=normalized_name, account_ids=normalized_account_ids)
                await self._repository.add(
                    AccountGroup(
                        id=group_id,
                        name=normalized_name,
                        created_at=utcnow(),
                        account_assignments=[
                            AccountGroupAccount(group_id=group_id, account_id=account_id)
                            for account_id in normalized_account_ids
                        ],
                        limits=_limit_rows(group_id, limits),
                    )
                )
                await self._repository.commit()
            except IntegrityError as exc:
                await self._repository.rollback()
                raise AccountGroupConflictError("Account group conflicts with an existing group") from exc

        return AccountGroupData(
            id=group_id,
            name=normalized_name,
            account_ids=sorted(normalized_account_ids),
            limits=_sorted_limits(limits),
        )

    async def update_group(
        self,
        group_id: str,
        *,
        name: str,
        account_ids: list[str],
        limits: list[LimitRuleInput],
    ) -> AccountGroupData:
        normalized_name = _normalize_name(name)
        normalized_account_ids = _normalize_account_ids(account_ids)
        _validate_limits(limits)
        now = utcnow()

        async with sqlite_writer_section():
            try:
                group = await self._repository.get_for_update(group_id)
                if group is None:
                    raise AccountGroupNotFoundError(f"Account group not found: {group_id}")
                await self._validate_mutation(
                    group_id=group_id,
                    name=normalized_name,
                    account_ids=normalized_account_ids,
                )
                linked_key_ids = await self._repository.linked_key_ids(group_id)
                group.name = normalized_name
                await self._repository.replace_assignments(group_id, normalized_account_ids)
                await self._repository.replace_limits(group_id, _limit_rows(group_id, limits))
                await self._api_keys_service.sync_group_limits(linked_key_ids, limits, now=now)
                await self._repository.commit()
            except IntegrityError as exc:
                await self._repository.rollback()
                raise AccountGroupConflictError("Account group conflicts with an existing group") from exc

        await self._invalidate_key_cache()
        return AccountGroupData(
            id=group_id,
            name=normalized_name,
            account_ids=sorted(normalized_account_ids),
            limits=_sorted_limits(limits),
            key_count=len(linked_key_ids),
        )

    async def delete_group(self, group_id: str) -> None:
        async with sqlite_writer_section():
            try:
                group = await self._repository.get_for_update(group_id)
                if group is None:
                    raise AccountGroupNotFoundError(f"Account group not found: {group_id}")
                if await self._repository.count_linked_keys(group_id):
                    raise AccountGroupConflictError("Account group is used by API keys")
                await self._repository.delete(group_id)
                await self._repository.commit()
            except IntegrityError as exc:
                await self._repository.rollback()
                raise AccountGroupConflictError("Account group is used by API keys") from exc

    async def _validate_mutation(self, *, group_id: str | None, name: str, account_ids: list[str]) -> None:
        existing_name = await self._repository.get_by_name(name, exclude_id=group_id)
        if existing_name is not None:
            raise AccountGroupConflictError(f"Account group name already exists: {name}")

        existing_account_ids = await self._repository.existing_account_ids(account_ids)
        missing_account_ids = [account_id for account_id in account_ids if account_id not in existing_account_ids]
        if missing_account_ids:
            raise AccountGroupValidationError(f"Unknown account ids: {', '.join(missing_account_ids)}")

        conflicts = await self._repository.conflicting_assignments(account_ids, exclude_group_id=group_id)
        if conflicts:
            account_ids_text = ", ".join(account_id for account_id, _ in conflicts)
            raise AccountGroupConflictError(f"Accounts already belong to another group: {account_ids_text}")

    async def _invalidate_key_cache(self) -> None:
        get_api_key_cache().clear()
        poller = get_cache_invalidation_poller()
        if poller is not None:
            await poller.bump(NAMESPACE_API_KEY)
