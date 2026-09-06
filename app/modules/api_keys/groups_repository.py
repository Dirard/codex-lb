from __future__ import annotations

from sqlalchemy import delete, func, insert, select
from sqlalchemy.ext.asyncio import AsyncSession

from app.db.models import Account, AccountGroup, AccountGroupAccount, AccountGroupLimit, ApiKey


class AccountGroupsRepository:
    def __init__(self, session: AsyncSession) -> None:
        self._session = session

    async def commit(self) -> None:
        await self._session.commit()

    async def rollback(self) -> None:
        await self._session.rollback()

    async def list_all(self) -> list[AccountGroup]:
        result = await self._session.execute(select(AccountGroup).order_by(AccountGroup.name))
        return list(result.scalars().all())

    async def key_counts_by_group(self) -> dict[str, int]:
        result = await self._session.execute(
            select(ApiKey.group_id, func.count())
            .where(ApiKey.group_id.is_not(None))
            .group_by(ApiKey.group_id)
        )
        return {group_id: count for group_id, count in result.all()}

    async def get_for_update(self, group_id: str) -> AccountGroup | None:
        result = await self._session.execute(
            select(AccountGroup).where(AccountGroup.id == group_id).with_for_update()
        )
        return result.scalar_one_or_none()

    async def get_by_name(self, name: str, *, exclude_id: str | None = None) -> AccountGroup | None:
        stmt = select(AccountGroup).where(AccountGroup.name == name)
        if exclude_id is not None:
            stmt = stmt.where(AccountGroup.id != exclude_id)
        result = await self._session.execute(stmt)
        return result.scalar_one_or_none()

    async def add(self, group: AccountGroup) -> None:
        self._session.add(group)

    async def delete(self, group_id: str) -> None:
        await self._session.execute(delete(AccountGroup).where(AccountGroup.id == group_id))

    async def existing_account_ids(self, account_ids: list[str]) -> set[str]:
        if not account_ids:
            return set()
        result = await self._session.execute(
            select(Account.id)
            .where(Account.id.in_(account_ids))
            .where(Account.delete_requested_at.is_(None))
        )
        return set(result.scalars().all())

    async def conflicting_assignments(
        self, account_ids: list[str], *, exclude_group_id: str | None
    ) -> list[tuple[str, str]]:
        if not account_ids:
            return []
        stmt = select(AccountGroupAccount.account_id, AccountGroupAccount.group_id).where(
            AccountGroupAccount.account_id.in_(account_ids)
        )
        if exclude_group_id is not None:
            stmt = stmt.where(AccountGroupAccount.group_id != exclude_group_id)
        result = await self._session.execute(stmt.order_by(AccountGroupAccount.account_id))
        return [(account_id, group_id) for account_id, group_id in result.all()]

    async def linked_key_ids(self, group_id: str) -> list[str]:
        stmt = select(ApiKey.id).where(ApiKey.group_id == group_id).order_by(ApiKey.id)
        if self._session.get_bind().dialect.name == "postgresql":
            stmt = stmt.with_for_update(key_share=True)
        result = await self._session.execute(stmt)
        return list(result.scalars().all())

    async def count_linked_keys(self, group_id: str) -> int:
        result = await self._session.execute(
            select(func.count()).select_from(ApiKey).where(ApiKey.group_id == group_id)
        )
        return int(result.scalar_one())

    async def replace_assignments(self, group_id: str, account_ids: list[str]) -> None:
        await self._session.execute(delete(AccountGroupAccount).where(AccountGroupAccount.group_id == group_id))
        if account_ids:
            await self._session.execute(
                insert(AccountGroupAccount),
                [{"group_id": group_id, "account_id": account_id} for account_id in account_ids],
            )

    async def replace_limits(self, group_id: str, limits: list[AccountGroupLimit]) -> None:
        await self._session.execute(delete(AccountGroupLimit).where(AccountGroupLimit.group_id == group_id))
        if not limits:
            return
        self._session.add_all(limits)
