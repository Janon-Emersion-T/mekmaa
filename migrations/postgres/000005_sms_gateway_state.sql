CREATE TABLE IF NOT EXISTS sms_gateway_state (
    id INTEGER PRIMARY KEY,
    latest_balance NUMERIC(12, 2),
    charged_from TEXT NOT NULL DEFAULT '',
    alerted_200 INTEGER NOT NULL DEFAULT 0,
    alerted_100 INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO sms_gateway_state (
    id,
    latest_balance,
    charged_from,
    alerted_200,
    alerted_100,
    updated_at
)
VALUES (
    1,
    NULL,
    '',
    0,
    0,
    CURRENT_TIMESTAMP
)
ON CONFLICT (id) DO NOTHING;
