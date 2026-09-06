from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest
from alembic import command
from sqlalchemy import create_engine, event, text
from sqlalchemy.engine import Connection, Engine

from app.db.migrate import _build_alembic_config, check_schema_drift, run_upgrade

pytestmark = pytest.mark.integration

_PARENT = "20260830_000000_add_quota_warmup_claim_expiry"
_REVISION = "20260906_000000_add_account_groups"


def _seed_legacy_rows(connection: Connection) -> None:
    for account_id in ("account-a", "account-b"):
        connection.execute(
            text(
                "INSERT INTO accounts (id, email, plan_type, access_token_encrypted, refresh_token_encrypted, "
                "id_token_encrypted, last_refresh, status, codex_installation_id) "
                "VALUES (:id, :email, 'plus', X'00', X'00', X'00', CURRENT_TIMESTAMP, 'active', :id)"
            ),
            {"id": account_id, "email": f"{account_id}@example.test"},
        )
    for key_id in ("key-a", "key-b"):
        connection.execute(
            text(
                "INSERT INTO api_keys (id, name, key_hash, key_prefix, is_active, account_assignment_scope_enabled) "
                "VALUES (:id, :id, :id, 'fixture', TRUE, TRUE)"
            ),
            {"id": key_id},
        )
        connection.execute(
            text("INSERT INTO api_key_accounts (api_key_id, account_id) VALUES (:id, 'account-a')"),
            {"id": key_id},
        )
    connection.execute(
        text(
            "INSERT INTO api_key_limits "
            "(id, api_key_id, limit_type, limit_window, max_value, current_value, reset_at) "
            "VALUES (1, 'key-a', 'credits', 'weekly', 1000, 320, '2026-09-07 00:00:00')"
        )
    )
    connection.execute(
        text(
            "INSERT INTO api_key_usage_reservations (id, api_key_id, model, status) "
            "VALUES ('reservation', 'key-a', 'fixture-model', 'reserved')"
        )
    )
    connection.execute(
        text(
            "INSERT INTO api_key_usage_reservation_items "
            "(reservation_id, limit_id, limit_type, reserved_delta, expected_reset_at) "
            "VALUES ('reservation', 1, 'credits', 20, '2026-09-07 00:00:00')"
        )
    )


def _assert_ledger_unchanged(connection: Connection) -> None:
    assert connection.execute(text("SELECT current_value, max_value FROM api_key_limits")).one() == (320, 1000)
    assert connection.execute(text("SELECT id, status FROM api_key_usage_reservations")).one() == (
        "reservation",
        "reserved",
    )
    assert connection.execute(text("SELECT limit_id, reserved_delta FROM api_key_usage_reservation_items")).one() == (
        1,
        20,
    )
    assert connection.execute(text("PRAGMA foreign_key_check")).all() == []


def test_account_groups_migration_preserves_policy_and_ledger(tmp_path: Path) -> None:
    url = f"sqlite:///{tmp_path / 'groups.sqlite'}"
    run_upgrade(url, _PARENT, bootstrap_legacy=True)
    engine = create_engine(url)
    with engine.begin() as connection:
        _seed_legacy_rows(connection)
    engine.dispose()

    def enforce_foreign_keys(dbapi_connection: sqlite3.Connection, _record: object) -> None:
        dbapi_connection.execute("PRAGMA foreign_keys=ON")

    event.listen(Engine, "connect", enforce_foreign_keys)
    try:
        run_upgrade(url, _REVISION, bootstrap_legacy=False)
        with engine.begin() as connection:
            assert connection.execute(text("PRAGMA foreign_keys")).scalar_one() == 1
            assert connection.execute(text("SELECT group_id FROM api_keys")).all() == [(None,), (None,)]
            _assert_ledger_unchanged(connection)
            connection.execute(
                text("INSERT INTO account_groups (id, name) VALUES ('group', 'Group'), ('empty', 'Empty')")
            )
            connection.execute(
                text("INSERT INTO account_group_accounts (account_id, group_id) VALUES ('account-b', 'group')")
            )
            connection.execute(text("UPDATE api_keys SET group_id = 'group' WHERE id = 'key-a'"))
            connection.execute(text("UPDATE api_keys SET group_id = 'empty' WHERE id = 'key-b'"))

        command.downgrade(_build_alembic_config(url), _PARENT)
        with engine.connect() as connection:
            _assert_ledger_unchanged(connection)
            assert connection.execute(text("SELECT api_key_id, account_id FROM api_key_accounts")).all() == [
                ("key-a", "account-b")
            ]
            assert connection.execute(text("SELECT account_assignment_scope_enabled FROM api_keys")).all() == [
                (1,),
                (1,),
            ]
            assert connection.execute(text("SELECT id, key_hash FROM api_keys ORDER BY id")).all() == [
                ("key-a", "key-a"),
                ("key-b", "key-b"),
            ]

        run_upgrade(url, _REVISION, bootstrap_legacy=False)
        with engine.connect() as connection:
            _assert_ledger_unchanged(connection)
            assert connection.execute(text("SELECT group_id FROM api_keys")).all() == [(None,), (None,)]
        assert not check_schema_drift(url)
    finally:
        event.remove(Engine, "connect", enforce_foreign_keys)
        engine.dispose()
