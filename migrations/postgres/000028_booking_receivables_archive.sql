-- Booking receivables archive metadata.
-- Booking, payment, receipt and finance records remain intact.

CREATE TABLE IF NOT EXISTS booking_receivables_archives (
    schedule_id BIGINT PRIMARY KEY
        REFERENCES space_schedules(id) ON DELETE CASCADE,
    archived_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_by_user_id BIGINT REFERENCES users(id),
    archive_reason TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_booking_receivables_archives_archived_at
    ON booking_receivables_archives (archived_at DESC);
