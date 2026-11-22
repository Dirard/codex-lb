package sqlite

// schemaV52 stores the sticky-only option of an account: 1 keeps the
// conversations it owns on it when its limit runs out. Additive: older code
// neither reads nor writes the column, its account upsert leaves it as it is,
// and adding a column with a constant default rewrites no table.
const schemaV52 = `
ALTER TABLE accounts ADD COLUMN sticky_only INTEGER NOT NULL DEFAULT 0 CHECK(sticky_only IN (0,1));
`
