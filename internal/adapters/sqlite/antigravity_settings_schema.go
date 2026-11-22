package sqlite

// schemaV53 stores the Antigravity client version of the Cloud Code
// User-Agent. Additive: older code neither reads nor writes the column, its
// settings update leaves it as it is, and adding a column with a constant
// default rewrites no table.
const schemaV53 = `
ALTER TABLE runtime_settings ADD COLUMN antigravity_client_version TEXT NOT NULL DEFAULT '2.34.0' CHECK(length(antigravity_client_version) BETWEEN 5 AND 20);
`

// schemaV54 corrects the V53 seed to the real Antigravity Hub version
// 2.33.0; 2.34.0 was an invented future version. Operators can still save any
// version in Settings. Additive: one row, a value the previous code accepts.
const schemaV54 = `
UPDATE runtime_settings SET antigravity_client_version='2.33.0' WHERE antigravity_client_version='2.34.0';
`
