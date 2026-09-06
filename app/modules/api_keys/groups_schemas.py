from __future__ import annotations

from pydantic import Field

from app.modules.api_keys.schemas import LimitRuleCreate
from app.modules.shared.schemas import DashboardModel


class AccountGroupSaveRequest(DashboardModel):
    name: str = Field(min_length=1, max_length=128)
    account_ids: list[str] = Field(default_factory=list)
    limits: list[LimitRuleCreate] = Field(default_factory=list)


class AccountGroupResponse(DashboardModel):
    id: str
    name: str
    account_ids: list[str]
    limits: list[LimitRuleCreate]
    key_count: int
