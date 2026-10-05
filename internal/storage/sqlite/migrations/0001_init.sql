CREATE TABLE users (
    id         INTEGER PRIMARY KEY,
    tg_id      INTEGER NOT NULL UNIQUE,
    username   TEXT    NOT NULL DEFAULT '',
    name       TEXT    NOT NULL DEFAULT '',
    demo_until INTEGER,
    created_at INTEGER NOT NULL
);

CREATE TABLE messages (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    role         TEXT    NOT NULL,
    content      TEXT    NOT NULL DEFAULT '',
    tool_calls   TEXT,
    tool_call_id TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);

CREATE INDEX messages_user ON messages(user_id, id);
