package sqlite

// Route revisions are operational identity, separate from deletion generation
// so an edited source retains its reservations and historical usage.
const schemaV32 = `
ALTER TABLE accounts ADD COLUMN route_revision INTEGER NOT NULL DEFAULT 0 CHECK(route_revision>=0);
ALTER TABLE usage_reservations ADD COLUMN route_revision INTEGER NOT NULL DEFAULT 0 CHECK(route_revision>=0);
`
