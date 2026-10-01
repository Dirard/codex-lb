package sqlite

const schemaV30 = `ALTER TABLE continuations ADD COLUMN turn_state_forwardable INTEGER NOT NULL DEFAULT 0 CHECK(turn_state_forwardable IN (0,1));`
