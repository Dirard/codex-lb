from __future__ import annotations

from fastapi import APIRouter, Body, Depends, Response

from app.core.auth.dependencies import (
    require_dashboard_write_access,
    set_dashboard_error_format,
    validate_dashboard_session,
)
from app.core.exceptions import (
    DashboardBadRequestError,
    DashboardConflictError,
    DashboardNotFoundError,
)
from app.dependencies import AccountGroupsContext, get_account_groups_context
from app.modules.api_keys.groups_schemas import AccountGroupResponse, AccountGroupSaveRequest
from app.modules.api_keys.groups_service import (
    AccountGroupConflictError,
    AccountGroupData,
    AccountGroupNotFoundError,
    AccountGroupValidationError,
)
from app.modules.api_keys.schemas import LimitRuleCreate
from app.modules.api_keys.service import LimitRuleInput

router = APIRouter(
    prefix="/api/account-groups",
    tags=["dashboard"],
    dependencies=[Depends(validate_dashboard_session), Depends(set_dashboard_error_format)],
)


def _to_response(group: AccountGroupData) -> AccountGroupResponse:
    return AccountGroupResponse(
        id=group.id,
        name=group.name,
        account_ids=group.account_ids,
        limits=[
            LimitRuleCreate(
                limit_type=limit.limit_type,
                limit_window=limit.limit_window,
                max_value=limit.max_value,
                model_filter=limit.model_filter,
            )
            for limit in group.limits
        ],
        key_count=group.key_count,
    )


def _limit_inputs(payload: AccountGroupSaveRequest) -> list[LimitRuleInput]:
    return [
        LimitRuleInput(
            limit_type=limit.limit_type,
            limit_window=limit.limit_window,
            max_value=limit.max_value,
            model_filter=limit.model_filter,
        )
        for limit in payload.limits
    ]


@router.get("", response_model=list[AccountGroupResponse], include_in_schema=False)
@router.get("/", response_model=list[AccountGroupResponse])
async def list_account_groups(
    context: AccountGroupsContext = Depends(get_account_groups_context),
) -> list[AccountGroupResponse]:
    return [_to_response(group) for group in await context.service.list_groups()]


@router.post("", response_model=AccountGroupResponse, include_in_schema=False)
@router.post("/", response_model=AccountGroupResponse)
async def create_account_group(
    payload: AccountGroupSaveRequest = Body(...),
    _write_access=Depends(require_dashboard_write_access),
    context: AccountGroupsContext = Depends(get_account_groups_context),
) -> AccountGroupResponse:
    try:
        group = await context.service.create_group(
            name=payload.name,
            account_ids=payload.account_ids,
            limits=_limit_inputs(payload),
        )
    except AccountGroupValidationError as exc:
        raise DashboardBadRequestError(str(exc), code="invalid_account_group_payload") from exc
    except AccountGroupConflictError as exc:
        raise DashboardConflictError(str(exc)) from exc
    return _to_response(group)


@router.put("/{group_id}", response_model=AccountGroupResponse)
async def update_account_group(
    group_id: str,
    payload: AccountGroupSaveRequest = Body(...),
    _write_access=Depends(require_dashboard_write_access),
    context: AccountGroupsContext = Depends(get_account_groups_context),
) -> AccountGroupResponse:
    try:
        group = await context.service.update_group(
            group_id,
            name=payload.name,
            account_ids=payload.account_ids,
            limits=_limit_inputs(payload),
        )
    except AccountGroupNotFoundError as exc:
        raise DashboardNotFoundError(str(exc)) from exc
    except AccountGroupValidationError as exc:
        raise DashboardBadRequestError(str(exc), code="invalid_account_group_payload") from exc
    except AccountGroupConflictError as exc:
        raise DashboardConflictError(str(exc)) from exc
    return _to_response(group)


@router.delete("/{group_id}")
async def delete_account_group(
    group_id: str,
    _write_access=Depends(require_dashboard_write_access),
    context: AccountGroupsContext = Depends(get_account_groups_context),
) -> Response:
    try:
        await context.service.delete_group(group_id)
    except AccountGroupNotFoundError as exc:
        raise DashboardNotFoundError(str(exc)) from exc
    except AccountGroupConflictError as exc:
        raise DashboardConflictError(str(exc)) from exc
    return Response(status_code=204)
