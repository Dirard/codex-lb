package sqlite

// schemaV53 stores the Antigravity client version of the Cloud Code
// User-Agent. Additive: older code neither reads nor writes the column, its
// settings update leaves it as it is, and adding a column with a constant
// default rewrites no table.
const schemaV53 = `
ALTER TABLE runtime_settings ADD COLUMN antigravity_client_version TEXT NOT NULL DEFAULT '2.34.0' CHECK(length(antigravity_client_version) BETWEEN 5 AND 20);
`
