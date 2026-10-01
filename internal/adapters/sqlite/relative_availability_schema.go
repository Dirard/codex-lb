package sqlite

const schemaV24 = `
ALTER TABLE runtime_settings ADD COLUMN relative_availability_power REAL NOT NULL DEFAULT 2.0 CHECK(relative_availability_power > 0);
ALTER TABLE runtime_settings ADD COLUMN relative_availability_top_k INTEGER NOT NULL DEFAULT 5 CHECK(relative_availability_top_k BETWEEN 1 AND 20);
`
