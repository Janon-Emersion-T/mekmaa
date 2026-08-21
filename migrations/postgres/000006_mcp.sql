CREATE TABLE IF NOT EXISTS mcp_customers (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    email TEXT NOT NULL UNIQUE,
    phone TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (btrim(name) <> ''),
    CHECK (btrim(email) <> '')
);

CREATE TABLE IF NOT EXISTS mcp_pricing_bands (
    id BIGSERIAL PRIMARY KEY,
    tier TEXT NOT NULL,
    minimum_sessions INTEGER NOT NULL,
    maximum_sessions INTEGER NOT NULL DEFAULT 0,
    price_per_session NUMERIC(12,2) NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    effective_from DATE,
    effective_to DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (tier IN ('weekday_offpeak', 'weekday_peak', 'weekend_offpeak', 'weekend_peak')),
    CHECK (minimum_sessions >= 1),
    CHECK (maximum_sessions = 0 OR maximum_sessions >= minimum_sessions),
    CHECK (price_per_session > 0),
    CHECK (effective_to IS NULL OR effective_from IS NULL OR effective_to >= effective_from)
);

CREATE TABLE IF NOT EXISTS mcp_monthly_plans (
    id BIGSERIAL PRIMARY KEY,
    customer_id BIGINT NOT NULL REFERENCES mcp_customers(id) ON DELETE CASCADE,
    plan_month DATE NOT NULL,
    game_id BIGINT NOT NULL DEFAULT 0,
    activity TEXT NOT NULL,
    quantity INTEGER NOT NULL DEFAULT 1,
    title TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    total_sessions INTEGER NOT NULL DEFAULT 0,
    gross_amount NUMERIC(12,2) NOT NULL DEFAULT 0,
    total_collected NUMERIC(12,2) NOT NULL DEFAULT 0,
    outstanding_amount NUMERIC(12,2) NOT NULL DEFAULT 0,
    payment_status TEXT NOT NULL DEFAULT 'unpaid',
    notes TEXT NOT NULL DEFAULT '',
    created_by_user_id BIGINT REFERENCES users(id),
    requested_by_user_id BIGINT REFERENCES users(id),
    confirmed_at TIMESTAMPTZ,
    confirmed_by_user_id BIGINT REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (date_trunc('month', plan_month) = plan_month),
    CHECK (btrim(activity) <> ''),
    CHECK (quantity >= 1),
    CHECK (btrim(title) <> ''),
    CHECK (status IN ('draft', 'pending', 'confirmed', 'cancelled', 'completed')),
    CHECK (total_sessions >= 0),
    CHECK (gross_amount >= 0),
    CHECK (total_collected >= 0),
    CHECK (outstanding_amount >= 0),
    CHECK (payment_status IN ('not_applicable', 'unpaid', 'partially_paid', 'paid')),
    CHECK (gross_amount >= total_collected),
    CHECK (outstanding_amount = gross_amount - total_collected)
);

CREATE TABLE IF NOT EXISTS mcp_plan_rules (
    id BIGSERIAL PRIMARY KEY,
    plan_id BIGINT NOT NULL REFERENCES mcp_monthly_plans(id) ON DELETE CASCADE,
    weekday INTEGER NOT NULL,
    start_hour TIME NOT NULL,
    end_hour TIME NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (weekday BETWEEN 0 AND 6),
    CHECK (end_hour > start_hour)
);

CREATE TABLE IF NOT EXISTS mcp_plan_sessions (
    id BIGSERIAL PRIMARY KEY,
    plan_id BIGINT NOT NULL REFERENCES mcp_monthly_plans(id) ON DELETE CASCADE,
    session_date DATE NOT NULL,
    session_hour TIME NOT NULL,
    activity TEXT NOT NULL,
    quantity INTEGER NOT NULL DEFAULT 1,
    pricing_tier TEXT NOT NULL,
    pricing_band_id BIGINT REFERENCES mcp_pricing_bands(id),
    pricing_band_minimum INTEGER NOT NULL DEFAULT 0,
    pricing_band_maximum INTEGER NOT NULL DEFAULT 0,
    price_per_session NUMERIC(12,2) NOT NULL DEFAULT 0,
    amount NUMERIC(12,2) NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending',
    conflict_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (btrim(activity) <> ''),
    CHECK (quantity >= 1),
    CHECK (pricing_tier IN ('weekday_offpeak', 'weekday_peak', 'weekend_offpeak', 'weekend_peak')),
    CHECK (pricing_band_minimum >= 0),
    CHECK (pricing_band_maximum = 0 OR pricing_band_maximum >= pricing_band_minimum),
    CHECK (price_per_session >= 0),
    CHECK (amount >= 0),
    CHECK (status IN ('draft', 'pending', 'confirmed', 'cancelled', 'completed'))
);

CREATE TABLE IF NOT EXISTS mcp_payment_collections (
    id BIGSERIAL PRIMARY KEY,
    plan_id BIGINT NOT NULL REFERENCES mcp_monthly_plans(id) ON DELETE CASCADE,
    finance_transaction_id BIGINT NOT NULL UNIQUE REFERENCES finance_transactions(id),
    amount NUMERIC(12,2) NOT NULL,
    payment_method TEXT NOT NULL DEFAULT 'cash',
    payment_note TEXT NOT NULL DEFAULT '',
    collected_by_user_id BIGINT REFERENCES users(id),
    collected_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    voided BOOLEAN NOT NULL DEFAULT FALSE,
    void_reason TEXT NOT NULL DEFAULT '',
    voided_by_user_id BIGINT REFERENCES users(id),
    voided_at TIMESTAMPTZ,
    CHECK (amount > 0),
    CHECK (payment_method IN ('cash', 'bank_transfer', 'card', 'cheque', 'online', 'other')),
    CHECK ((voided = FALSE AND voided_at IS NULL) OR (voided = TRUE))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_pricing_band_unique_range
    ON mcp_pricing_bands (
        tier,
        minimum_sessions,
        maximum_sessions,
        COALESCE(effective_from, DATE '0001-01-01'),
        COALESCE(effective_to, DATE '9999-12-31')
    );

CREATE INDEX IF NOT EXISTS idx_mcp_plans_customer_month
    ON mcp_monthly_plans (customer_id, plan_month DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_mcp_plans_status
    ON mcp_monthly_plans (status, plan_month DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_mcp_sessions_slot
    ON mcp_plan_sessions (session_date, session_hour, status);

CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_plan_sessions_unique_slot
    ON mcp_plan_sessions (plan_id, session_date, session_hour);

CREATE INDEX IF NOT EXISTS idx_mcp_payments_plan
    ON mcp_payment_collections (plan_id, collected_at DESC, id DESC);
