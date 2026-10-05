-- A deleted slot is neither offered nor booked; its bookings stay on it.
CREATE TABLE slots (
    id         INTEGER PRIMARY KEY,
    starts_at  INTEGER NOT NULL,
    deleted_at INTEGER
);

CREATE UNIQUE INDEX slots_starts_at ON slots(starts_at);

-- Reminders due by reminded_at are sent or skipped; a booking, a new slot and a reminder set it to now.
-- version rises on every change; the sheet shows the booking as of synced_version.
CREATE TABLE bookings (
    id             INTEGER PRIMARY KEY,
    user_id        INTEGER NOT NULL REFERENCES users(id),
    slot_id        INTEGER NOT NULL REFERENCES slots(id),
    grade          INTEGER NOT NULL,
    goal           TEXT    NOT NULL,
    phone          TEXT    NOT NULL,
    status         TEXT    NOT NULL,
    created_at     INTEGER NOT NULL,
    reminded_at    INTEGER NOT NULL,
    version        INTEGER NOT NULL DEFAULT 1,
    synced_version INTEGER NOT NULL DEFAULT 0
);

-- One live booking per slot, new or confirmed; a cancelled one frees it.
CREATE UNIQUE INDEX bookings_slot_active ON bookings(slot_id) WHERE status IN ('new', 'confirmed');
CREATE INDEX bookings_user ON bookings(user_id, id);
