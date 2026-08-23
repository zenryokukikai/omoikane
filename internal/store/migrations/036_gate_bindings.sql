-- External gate provisioning (issue #104 slice G2): omoikane registers
-- each personal librarian as one gate instance on the opencrab admin
-- plane, and (slice G3) each /talk thread as one gate binding.
--
-- user_librarians.gate_instance_id is the registered instance's UUIDv7;
-- '' means not registered yet (feature off, or the subject resolver is
-- still upstream work — opencrab#763).
--
-- talk_gate_bindings is the single source of truth for the
-- thread ↔ binding correspondence: one row per /talk thread that has a
-- gate binding. binding_id/instance_id are the admin plane's UUIDs.
--
-- NOTE: shipped migration version numbers are never reusable.
ALTER TABLE user_librarians ADD COLUMN gate_instance_id TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS talk_gate_bindings (
    thread_id   TEXT PRIMARY KEY,
    binding_id  TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
