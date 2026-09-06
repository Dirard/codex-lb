"""Add optional account groups without changing existing key policy or usage."""

from __future__ import annotations

import sqlalchemy as sa
from alembic import op
from sqlalchemy.dialects import postgresql

revision = "20260906_000000_add_account_groups"
down_revision = "20260830_000000_add_quota_warmup_claim_expiry"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "account_groups",
        sa.Column("id", sa.String(), primary_key=True),
        sa.Column("name", sa.String(128), nullable=False, unique=True),
        sa.Column("created_at", sa.DateTime(), nullable=False, server_default=sa.func.now()),
        if_not_exists=True,
    )
    op.create_table(
        "account_group_accounts",
        sa.Column("account_id", sa.String(), sa.ForeignKey("accounts.id", ondelete="CASCADE"), primary_key=True),
        sa.Column("group_id", sa.String(), sa.ForeignKey("account_groups.id", ondelete="CASCADE"), nullable=False),
        if_not_exists=True,
    )
    op.create_index("ix_account_group_accounts_group_id", "account_group_accounts", ["group_id"], if_not_exists=True)
    # Reuse existing PostgreSQL enums; do not create or drop shared enum types.
    limit_type = sa.String().with_variant(
        postgresql.ENUM(
            "total_tokens",
            "input_tokens",
            "output_tokens",
            "cost_usd",
            "credits",
            name="limit_type",
            create_type=False,
        ),
        "postgresql",
    )
    limit_window = sa.String().with_variant(
        postgresql.ENUM("daily", "weekly", "monthly", "5h", "7d", name="limit_window", create_type=False),
        "postgresql",
    )
    op.create_table(
        "account_group_limits",
        sa.Column("id", sa.Integer(), primary_key=True, autoincrement=True),
        sa.Column("group_id", sa.String(), sa.ForeignKey("account_groups.id", ondelete="CASCADE"), nullable=False),
        sa.Column("limit_type", limit_type, nullable=False),
        sa.Column("limit_window", limit_window, nullable=False),
        sa.Column("max_value", sa.BigInteger(), nullable=False),
        sa.Column("model_filter", sa.String(), nullable=True),
        sa.UniqueConstraint("group_id", "limit_type", "limit_window", "model_filter", name="uq_account_group_limit"),
        sa.CheckConstraint("max_value > 0", name="ck_account_group_limit_positive"),
        if_not_exists=True,
    )
    op.create_index(
        "uq_account_group_limit_unfiltered",
        "account_group_limits",
        ["group_id", "limit_type", "limit_window"],
        unique=True,
        sqlite_where=sa.text("model_filter IS NULL"),
        postgresql_where=sa.text("model_filter IS NULL"),
        if_not_exists=True,
    )
    if "group_id" in {column["name"] for column in sa.inspect(op.get_bind()).get_columns("api_keys")}:
        op.create_index("ix_api_keys_group_id", "api_keys", ["group_id"], if_not_exists=True)
        return
    if op.get_bind().dialect.name == "sqlite":
        # Native ADD COLUMN avoids recreating api_keys and cascading its child
        # rows when SQLite foreign-key enforcement is enabled.
        op.execute(
            "ALTER TABLE api_keys ADD COLUMN group_id VARCHAR "
            "CONSTRAINT fk_api_keys_group_id REFERENCES account_groups(id)"
        )
    else:
        op.add_column(
            "api_keys",
            sa.Column(
                "group_id",
                sa.String(),
                sa.ForeignKey("account_groups.id", name="fk_api_keys_group_id"),
                nullable=True,
            ),
        )
    op.create_index("ix_api_keys_group_id", "api_keys", ["group_id"], if_not_exists=True)


def downgrade() -> None:
    # Preserve the latest inherited scope as ordinary key assignments. Limit
    # ledgers already hold each key's effective rules and must not be rewritten.
    op.execute("DELETE FROM api_key_accounts WHERE api_key_id IN (SELECT id FROM api_keys WHERE group_id IS NOT NULL)")
    op.execute(
        "INSERT INTO api_key_accounts (api_key_id, account_id) "
        "SELECT k.id, a.account_id FROM api_keys k "
        "JOIN account_group_accounts a ON a.group_id = k.group_id"
    )
    op.execute("UPDATE api_keys SET account_assignment_scope_enabled = TRUE WHERE group_id IS NOT NULL")
    op.drop_index("ix_api_keys_group_id", table_name="api_keys")
    if op.get_bind().dialect.name == "sqlite":
        # SQLite >= 3.35 supports native column removal, preserving child rows.
        op.execute("ALTER TABLE api_keys DROP COLUMN group_id")
    else:
        op.drop_column("api_keys", "group_id")
    op.drop_table("account_group_limits")
    op.drop_table("account_group_accounts")
    op.drop_table("account_groups")
