ALTER TABLE space_schedules ADD COLUMN duration_minutes INTEGER NOT NULL DEFAULT 60 CHECK (duration_minutes IN (30, 60));
