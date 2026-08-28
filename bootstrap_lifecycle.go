package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

func runMigrations(db *sql.DB) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`ALTER TABLE users ADD COLUMN email_verified_at DATETIME`,
		`CREATE TABLE IF NOT EXISTS roles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE
		)`,
		`CREATE TABLE IF NOT EXISTS user_roles (
			user_id INTEGER NOT NULL,
			role_id INTEGER NOT NULL,
			PRIMARY KEY (user_id, role_id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS role_permissions (
			role_id INTEGER NOT NULL,
			permission TEXT NOT NULL,
			PRIMARY KEY (role_id, permission),
			FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS email_verifications (
			user_id INTEGER PRIMARY KEY,
			otp_hash TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS admissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			student_id TEXT NOT NULL UNIQUE,
			full_name TEXT NOT NULL,
			admission_date TEXT NOT NULL,
			date_of_birth TEXT NOT NULL,
			gender TEXT NOT NULL,
			practice_type TEXT NOT NULL DEFAULT 'group_practice',
			address TEXT NOT NULL,
			passport_number TEXT NOT NULL,
			school TEXT NOT NULL,
			guardian_name TEXT NOT NULL,
			guardian_relationship TEXT NOT NULL,
			guardian_contact_number TEXT NOT NULL,
			guardian_alternative_contact_number TEXT NOT NULL,
			medical_information TEXT NOT NULL,
			free_admission INTEGER NOT NULL DEFAULT 0,
			free_monthly_fee INTEGER NOT NULL DEFAULT 0,
			payment_collected INTEGER NOT NULL DEFAULT 0,
			payment_collected_at DATETIME,
			admission_payment_amount REAL NOT NULL DEFAULT 0,
			finance_transaction_id INTEGER,
			training_program_id INTEGER,
			photo_path TEXT NOT NULL DEFAULT '',
			qr_code_path TEXT NOT NULL DEFAULT '',
			qr_code_value TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS admission_training_programs (
			admission_id INTEGER NOT NULL,
			training_program_id INTEGER NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (admission_id, training_program_id),
			FOREIGN KEY (admission_id) REFERENCES admissions(id) ON DELETE CASCADE,
			FOREIGN KEY (training_program_id) REFERENCES training_programs(id)
		)`,
		`CREATE TABLE IF NOT EXISTS student_enrollments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			admission_id INTEGER NOT NULL,
			training_program_id INTEGER NOT NULL,
			enrollment_date TEXT NOT NULL DEFAULT '',
			free_admission INTEGER NOT NULL DEFAULT 0,
			free_monthly_fee INTEGER NOT NULL DEFAULT 0,
			discounted_monthly_fee REAL NOT NULL DEFAULT 0,
			payment_collected INTEGER NOT NULL DEFAULT 0,
			payment_collected_at DATETIME,
			admission_payment_amount REAL NOT NULL DEFAULT 0,
			finance_transaction_id INTEGER,
			active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(admission_id, training_program_id),
			FOREIGN KEY (admission_id) REFERENCES admissions(id) ON DELETE CASCADE,
			FOREIGN KEY (training_program_id) REFERENCES training_programs(id),
			FOREIGN KEY (finance_transaction_id) REFERENCES finance_transactions(id)
		)`,
		`CREATE TABLE IF NOT EXISTS student_enrollment_leaves (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			enrollment_id INTEGER NOT NULL,
			start_date TEXT NOT NULL,
			end_date TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (enrollment_id) REFERENCES student_enrollments(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS student_groups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			code TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL,
			training_program_id INTEGER,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (training_program_id) REFERENCES training_programs(id)
		)`,

		`CREATE TABLE IF NOT EXISTS student_group_members (
			group_id INTEGER NOT NULL,
			admission_id INTEGER NOT NULL,
			PRIMARY KEY (group_id, admission_id),
			FOREIGN KEY (group_id) REFERENCES student_groups(id) ON DELETE CASCADE,
			FOREIGN KEY (admission_id) REFERENCES admissions(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS student_group_coaches (
			group_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (group_id, user_id),
			FOREIGN KEY (group_id) REFERENCES student_groups(id) ON DELETE CASCADE,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS student_group_staff (
			group_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			assignment_role TEXT NOT NULL,
			primary_assignment INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (group_id, user_id, assignment_role),
			FOREIGN KEY (group_id) REFERENCES student_groups(id) ON DELETE CASCADE,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_student_group_staff_user
			ON student_group_staff(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_student_group_staff_group
			ON student_group_staff(group_id)`,
		`INSERT OR IGNORE INTO student_group_staff (
			group_id,
			user_id,
			assignment_role,
			primary_assignment,
			created_at,
			updated_at
		)
		SELECT
			group_id,
			user_id,
			'coach',
			0,
			created_at,
			created_at
		FROM student_group_coaches`,
		`CREATE TABLE IF NOT EXISTS student_group_sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			group_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			day_of_week TEXT NOT NULL,
			start_time TEXT NOT NULL,
			end_time TEXT NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (group_id) REFERENCES student_groups(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS coach_profiles (
			user_id INTEGER PRIMARY KEY,
			phone TEXT NOT NULL DEFAULT '',
			address TEXT NOT NULL DEFAULT '',
			specialties TEXT NOT NULL DEFAULT '',
			notes TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			coach_type TEXT NOT NULL DEFAULT 'main',
			parent_coach_id INTEGER,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY (parent_coach_id) REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS coach_attendance_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			attendance_date TEXT NOT NULL,
			status TEXT NOT NULL,
			note TEXT NOT NULL DEFAULT '',
			recorded_by_user_id INTEGER,
			recorded_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY (recorded_by_user_id) REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS attendance_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			group_id INTEGER NOT NULL,
			session_id INTEGER,
			admission_id INTEGER NOT NULL,
			attendance_date TEXT NOT NULL,
			status TEXT NOT NULL,
			note TEXT NOT NULL DEFAULT '',
			recorded_by_user_id INTEGER,
			recorded_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (group_id) REFERENCES student_groups(id) ON DELETE CASCADE,
			FOREIGN KEY (session_id) REFERENCES student_group_sessions(id) ON DELETE CASCADE,
			FOREIGN KEY (admission_id) REFERENCES admissions(id) ON DELETE CASCADE,
			FOREIGN KEY (recorded_by_user_id) REFERENCES users(id)
		)`,

		`CREATE TABLE IF NOT EXISTS courts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			code TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS court_activities (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			court_id INTEGER NOT NULL,
			activity TEXT NOT NULL,
			display_name TEXT NOT NULL,
			max_quantity INTEGER NOT NULL DEFAULT 1,
			auto_accept INTEGER NOT NULL DEFAULT 0,
			active INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(court_id, activity),
			FOREIGN KEY (court_id) REFERENCES courts(id) ON DELETE CASCADE
		)`,

		`CREATE TABLE IF NOT EXISTS court_layouts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			court_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(court_id, name),
			FOREIGN KEY (court_id) REFERENCES courts(id) ON DELETE CASCADE
		)`,

		`CREATE TABLE IF NOT EXISTS court_layout_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			layout_id INTEGER NOT NULL,
			activity TEXT NOT NULL,
			quantity INTEGER NOT NULL,
			UNIQUE(layout_id, activity),
			FOREIGN KEY (layout_id) REFERENCES court_layouts(id) ON DELETE CASCADE
		)`,

		`CREATE TABLE IF NOT EXISTS court_closures (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	court_id INTEGER NOT NULL,
	closure_date TEXT NOT NULL,
	start_hour TEXT NOT NULL,
	end_hour TEXT NOT NULL,
	activity TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL,
	reason TEXT NOT NULL DEFAULT '',
	active INTEGER NOT NULL DEFAULT 1,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	FOREIGN KEY (court_id) REFERENCES courts(id) ON DELETE CASCADE
)`,

		`CREATE TABLE IF NOT EXISTS pricing_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			activity TEXT NOT NULL,
			quantity INTEGER NOT NULL,
			weekday_offpeak_price REAL NOT NULL DEFAULT 0,
			weekday_peak_price REAL NOT NULL DEFAULT 0,
			weekend_offpeak_price REAL NOT NULL DEFAULT 0,
			weekend_peak_price REAL NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS pricing_settings (
			id INTEGER PRIMARY KEY,
			peak_start_hour TEXT NOT NULL,
			peak_end_hour TEXT NOT NULL,
			referral_commission_amount REAL NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS admission_pricing (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			practice_type TEXT NOT NULL UNIQUE,
			price REAL NOT NULL DEFAULT 0,
			monthly_fee REAL NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS training_programs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			activity TEXT NOT NULL,
			training_format TEXT NOT NULL,
			admission_fee REAL NOT NULL DEFAULT 0,
			monthly_fee REAL NOT NULL DEFAULT 0,
			active INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS finance_transactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			receipt_number TEXT NOT NULL UNIQUE,
			category TEXT NOT NULL,
			reference_type TEXT NOT NULL,
			reference_id INTEGER,
			person_name TEXT NOT NULL,
			description TEXT NOT NULL,
			payment_method TEXT NOT NULL DEFAULT 'cash',
			amount REAL NOT NULL DEFAULT 0,
			recorded_by_user_id INTEGER,
			recorded_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS finance_categories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			direction TEXT NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS student_monthly_payments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			admission_id INTEGER NOT NULL,
			enrollment_id INTEGER,
			payment_month TEXT NOT NULL,
			amount REAL NOT NULL DEFAULT 0,
			discount_amount REAL NOT NULL DEFAULT 0,
			adjustment_reason TEXT NOT NULL DEFAULT '',
			payment_method TEXT NOT NULL DEFAULT 'cash',
			finance_transaction_id INTEGER NOT NULL,
			collected_by_user_id INTEGER,
			collected_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (admission_id) REFERENCES admissions(id) ON DELETE CASCADE,
			FOREIGN KEY (enrollment_id) REFERENCES student_enrollments(id) ON DELETE CASCADE,
			FOREIGN KEY (finance_transaction_id) REFERENCES finance_transactions(id)
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			category TEXT NOT NULL,
			event_date TEXT NOT NULL,
			start_time TEXT,
			end_time TEXT,
			registration_deadline TEXT,
			venue TEXT NOT NULL,
			summary TEXT NOT NULL,
			image_path TEXT NOT NULL DEFAULT '',
			cta_label TEXT NOT NULL DEFAULT '',
			cta_link TEXT NOT NULL DEFAULT '',
			published INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS tournaments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			game_id INTEGER NOT NULL,
			participant_count INTEGER NOT NULL DEFAULT 0,
			entry_fee REAL NOT NULL DEFAULT 0,
			tournament_date TEXT NOT NULL DEFAULT '',
			entry_fee_finance_transaction_id INTEGER,
			entry_fee_finance_account_id INTEGER,
			entry_fee_recorded_at DATETIME,
			notes TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (game_id) REFERENCES games(id),
			FOREIGN KEY (entry_fee_finance_transaction_id) REFERENCES finance_transactions(id),
			FOREIGN KEY (entry_fee_finance_account_id) REFERENCES finance_accounts(id)
		)`,
		`CREATE TABLE IF NOT EXISTS tournament_sponsorships (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tournament_id INTEGER NOT NULL,
			sponsor_name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			amount REAL NOT NULL DEFAULT 0,
			finance_transaction_id INTEGER,
			finance_account_id INTEGER NOT NULL,
			recorded_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (tournament_id) REFERENCES tournaments(id) ON DELETE CASCADE,
			FOREIGN KEY (finance_transaction_id) REFERENCES finance_transactions(id),
			FOREIGN KEY (finance_account_id) REFERENCES finance_accounts(id)
		)`,
		`CREATE TABLE IF NOT EXISTS tournament_official_payments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tournament_id INTEGER NOT NULL,
			person_name TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			amount REAL NOT NULL DEFAULT 0,
			finance_transaction_id INTEGER,
			finance_account_id INTEGER NOT NULL,
			recorded_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (tournament_id) REFERENCES tournaments(id) ON DELETE CASCADE,
			FOREIGN KEY (finance_transaction_id) REFERENCES finance_transactions(id),
			FOREIGN KEY (finance_account_id) REFERENCES finance_accounts(id)
		)`,
		`CREATE TABLE IF NOT EXISTS tournament_expenses (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tournament_id INTEGER NOT NULL,
			expense_type TEXT NOT NULL,
			item_name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			amount REAL NOT NULL DEFAULT 0,
			finance_transaction_id INTEGER,
			finance_account_id INTEGER NOT NULL,
			recorded_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (tournament_id) REFERENCES tournaments(id) ON DELETE CASCADE,
			FOREIGN KEY (finance_transaction_id) REFERENCES finance_transactions(id),
			FOREIGN KEY (finance_account_id) REFERENCES finance_accounts(id)
		)`,
		`CREATE TABLE IF NOT EXISTS games (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			activity TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS referral_partners (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			code TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL DEFAULT '',
			phone TEXT NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS space_schedules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slot_date TEXT NOT NULL,
			slot_hour TEXT NOT NULL,
			entry_type TEXT NOT NULL,
			activity TEXT NOT NULL,
			quantity INTEGER NOT NULL,
			title TEXT NOT NULL,
			notes TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'confirmed',
			requester_name TEXT NOT NULL DEFAULT '',
			requester_email TEXT NOT NULL DEFAULT '',
			requester_phone TEXT NOT NULL DEFAULT '',
			requested_by_user_id INTEGER,
			review_note TEXT NOT NULL DEFAULT '',
			customer_message TEXT NOT NULL DEFAULT '',
			status_changed_at DATETIME,
			status_changed_by_user_id INTEGER,
			status_change_source TEXT NOT NULL DEFAULT '',
			cancellation_reason TEXT NOT NULL DEFAULT '',
			cancellation_finance_note TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS booking_referrals (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL UNIQUE,
			partner_id INTEGER NOT NULL,
			commission_amount REAL NOT NULL,
			paid INTEGER NOT NULL DEFAULT 0,
			paid_at DATETIME,
			payment_method TEXT NOT NULL DEFAULT '',
			finance_transaction_id INTEGER,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (partner_id) REFERENCES referral_partners(id)
		)`,
		`CREATE TABLE IF NOT EXISTS booking_financials (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL UNIQUE,
			quoted_amount REAL NOT NULL DEFAULT 0,
			paid INTEGER NOT NULL DEFAULT 0,
			paid_at DATETIME,
			payment_method TEXT NOT NULL DEFAULT '',
			finance_transaction_id INTEGER,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (finance_transaction_id) REFERENCES finance_transactions(id)
		)`,
		`CREATE TABLE IF NOT EXISTS booking_payment_collections (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL,
			finance_transaction_id INTEGER NOT NULL UNIQUE,
			amount REAL NOT NULL DEFAULT 0,
			payment_method TEXT NOT NULL DEFAULT 'cash',
			payment_note TEXT NOT NULL DEFAULT '',
			collected_by_user_id INTEGER,
			collected_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			voided INTEGER NOT NULL DEFAULT 0,
			void_reason TEXT NOT NULL DEFAULT '',
			voided_by_user_id INTEGER,
			voided_at DATETIME,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (finance_transaction_id) REFERENCES finance_transactions(id),
			FOREIGN KEY (collected_by_user_id) REFERENCES users(id),
			FOREIGN KEY (voided_by_user_id) REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS one_to_one_offerings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			game TEXT NOT NULL,
			audience TEXT NOT NULL,
			occurrence TEXT NOT NULL DEFAULT 'per_day',
			session_count INTEGER NOT NULL DEFAULT 1,
			price REAL NOT NULL DEFAULT 0,
			active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS one_to_one_bookings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL UNIQUE,
			offering_id INTEGER NOT NULL,
			customer_name TEXT NOT NULL,
			offering_name TEXT NOT NULL,
			game TEXT NOT NULL,
			audience TEXT NOT NULL,
			price REAL NOT NULL DEFAULT 0,
			discounted_price REAL NOT NULL DEFAULT -1,
			coach_fee REAL NOT NULL DEFAULT 0,
			sessions INTEGER NOT NULL DEFAULT 1,
			occurrence TEXT NOT NULL DEFAULT 'per_day',
			max_sessions INTEGER NOT NULL DEFAULT 1,
			coach_user_id INTEGER,
			package_status TEXT NOT NULL DEFAULT 'active',
			completed_sessions INTEGER NOT NULL DEFAULT 0,
			cancelled_sessions INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (offering_id) REFERENCES one_to_one_offerings(id),
			FOREIGN KEY (coach_user_id) REFERENCES users(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS one_to_one_booking_sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			booking_id INTEGER NOT NULL,
			schedule_id INTEGER NOT NULL UNIQUE,
			session_number INTEGER NOT NULL,
			coach_user_id INTEGER,
			coach_fee REAL NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'scheduled',
			attendance_status TEXT NOT NULL DEFAULT '',
			attendance_note TEXT NOT NULL DEFAULT '',
			attendance_marked_at DATETIME,
			attendance_marked_by_user_id INTEGER,
			completed_at DATETIME,
			completed_by_user_id INTEGER,
			cancelled_at DATETIME,
			notes TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (booking_id) REFERENCES one_to_one_bookings(id) ON DELETE CASCADE,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (coach_user_id) REFERENCES users(id) ON DELETE SET NULL,
			FOREIGN KEY (attendance_marked_by_user_id) REFERENCES users(id) ON DELETE SET NULL,
			FOREIGN KEY (completed_by_user_id) REFERENCES users(id) ON DELETE SET NULL,
			UNIQUE (booking_id, session_number)
		)`,
		`CREATE TABLE IF NOT EXISTS receipt_sequences (
			scope TEXT NOT NULL,
			year INTEGER NOT NULL,
			next_value INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (scope, year)
		)`,
		`CREATE TABLE IF NOT EXISTS booking_request_changes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL,
			previous_slot_date TEXT NOT NULL,
			previous_slot_hour TEXT NOT NULL,
			previous_activity TEXT NOT NULL,
			previous_quantity INTEGER NOT NULL,
			previous_quoted_price REAL NOT NULL DEFAULT 0,
			new_slot_date TEXT NOT NULL,
			new_slot_hour TEXT NOT NULL,
			new_activity TEXT NOT NULL,
			new_quantity INTEGER NOT NULL,
			new_quoted_price REAL NOT NULL DEFAULT 0,
			action_type TEXT NOT NULL,
			previous_status TEXT NOT NULL DEFAULT '',
			new_status TEXT NOT NULL DEFAULT '',
			change_source TEXT NOT NULL DEFAULT '',
			finance_note TEXT NOT NULL DEFAULT '',
			review_note TEXT NOT NULL DEFAULT '',
			customer_message TEXT NOT NULL DEFAULT '',
			changed_by_user_id INTEGER,
			changed_at DATETIME NOT NULL,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (changed_by_user_id) REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS booking_communications (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			related_event_type TEXT NOT NULL DEFAULT '',
			event_key TEXT NOT NULL,
			channel TEXT NOT NULL,
			recipient TEXT NOT NULL,
			subject TEXT NOT NULL DEFAULT '',
			body_preview TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			provider TEXT NOT NULL DEFAULT '',
			provider_message TEXT NOT NULL DEFAULT '',
			attempt_count INTEGER NOT NULL DEFAULT 0,
			last_attempt_at DATETIME,
			sent_at DATETIME,
			created_at DATETIME NOT NULL,
			created_by_user_id INTEGER,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (created_by_user_id) REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS booking_access_tokens (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL,
			public_id TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			purpose TEXT NOT NULL DEFAULT 'status',
			active INTEGER NOT NULL DEFAULT 1,
			expires_at DATETIME NOT NULL,
			last_accessed_at DATETIME,
			created_at DATETIME NOT NULL,
			revoked_at DATETIME,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS booking_cancellation_requests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			request_reason TEXT NOT NULL DEFAULT '',
			requested_at DATETIME NOT NULL,
			token_id INTEGER,
			review_note TEXT NOT NULL DEFAULT '',
			reviewed_at DATETIME,
			reviewed_by_user_id INTEGER,
			FOREIGN KEY (schedule_id) REFERENCES space_schedules(id) ON DELETE CASCADE,
			FOREIGN KEY (token_id) REFERENCES booking_access_tokens(id),
			FOREIGN KEY (reviewed_by_user_id) REFERENCES users(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_token_hash ON sessions(token_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_role_permissions_role_id ON role_permissions(role_id)`,
		`CREATE INDEX IF NOT EXISTS idx_email_verifications_expires_at ON email_verifications(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_admissions_created_at ON admissions(created_at)`,

		`CREATE INDEX IF NOT EXISTS idx_admission_training_programs_admission_id ON admission_training_programs(admission_id)`,
		`CREATE INDEX IF NOT EXISTS idx_admission_training_programs_program_id ON admission_training_programs(training_program_id)`,
		`CREATE INDEX IF NOT EXISTS idx_student_enrollments_admission_id ON student_enrollments(admission_id)`,
		`CREATE INDEX IF NOT EXISTS idx_student_enrollments_program_id ON student_enrollments(training_program_id)`,
		`CREATE INDEX IF NOT EXISTS idx_student_enrollments_active ON student_enrollments(active, admission_id)`,
		`CREATE INDEX IF NOT EXISTS idx_student_enrollment_leaves_enrollment_dates ON student_enrollment_leaves(enrollment_id, active, start_date, end_date)`,
		`CREATE INDEX IF NOT EXISTS idx_student_groups_created_at ON student_groups(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_student_group_members_group_id ON student_group_members(group_id)`,
		`CREATE INDEX IF NOT EXISTS idx_student_group_members_admission_id ON student_group_members(admission_id)`,
		`CREATE INDEX IF NOT EXISTS idx_student_group_sessions_group_id ON student_group_sessions(group_id, active, day_of_week, start_time)`,
		`CREATE INDEX IF NOT EXISTS idx_coach_profiles_active ON coach_profiles(active)`,
		`CREATE INDEX IF NOT EXISTS idx_coach_profiles_parent ON coach_profiles(parent_coach_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_coach_attendance_user_date ON coach_attendance_records(user_id, attendance_date)`,
		`CREATE INDEX IF NOT EXISTS idx_coach_attendance_date ON coach_attendance_records(attendance_date)`,
		`CREATE INDEX IF NOT EXISTS idx_attendance_group_date ON attendance_records(group_id, attendance_date)`,
		`CREATE INDEX IF NOT EXISTS idx_courts_active_order
		ON courts(active, sort_order, name)`,

		`CREATE INDEX IF NOT EXISTS idx_court_activities_court
		ON court_activities(court_id, active, sort_order)`,

		`CREATE INDEX IF NOT EXISTS idx_court_layouts_court
		ON court_layouts(court_id, active, sort_order)`,

		`CREATE INDEX IF NOT EXISTS idx_court_layout_items_layout
		ON court_layout_items(layout_id)`,
		`CREATE INDEX IF NOT EXISTS idx_court_closures_date
ON court_closures(closure_date, start_hour, end_hour)`,

		`CREATE INDEX IF NOT EXISTS idx_court_closures_court
ON court_closures(court_id, active, closure_date)`,

		`CREATE INDEX IF NOT EXISTS idx_court_closures_activity
ON court_closures(activity, active, closure_date)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_pricing_rules_option ON pricing_rules(activity, quantity)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_admission_pricing_type ON admission_pricing(practice_type)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_recorded_at ON finance_transactions(recorded_at)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_reference ON finance_transactions(reference_type, reference_id)`,
		`CREATE INDEX IF NOT EXISTS idx_admissions_finance_transaction_id ON admissions(finance_transaction_id)`,
		`CREATE INDEX IF NOT EXISTS idx_student_monthly_payments_finance_transaction_id ON student_monthly_payments(finance_transaction_id)`,
		`CREATE INDEX IF NOT EXISTS idx_student_monthly_payments_month ON student_monthly_payments(payment_month, collected_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_date ON events(event_date, start_time)`,
		`CREATE INDEX IF NOT EXISTS idx_events_published ON events(published, event_date)`,
		`CREATE INDEX IF NOT EXISTS idx_tournaments_date ON tournaments(tournament_date DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_tournaments_game ON tournaments(game_id, tournament_date DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_tournament_sponsorships_tournament ON tournament_sponsorships(tournament_id, recorded_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_tournament_official_payments_tournament ON tournament_official_payments(tournament_id, recorded_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_tournament_expenses_tournament ON tournament_expenses(tournament_id, recorded_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_games_active_order ON games(active, sort_order, name)`,
		`CREATE INDEX IF NOT EXISTS idx_space_schedules_slot ON space_schedules(slot_date, slot_hour)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_referrals_partner ON booking_referrals(partner_id, paid)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_financials_paid ON booking_financials(paid, schedule_id)`,
		`CREATE INDEX IF NOT EXISTS idx_one_to_one_offerings_active ON one_to_one_offerings(active, name, id)`,
		`CREATE INDEX IF NOT EXISTS idx_one_to_one_bookings_schedule ON one_to_one_bookings(schedule_id)`,
		`CREATE INDEX IF NOT EXISTS idx_one_to_one_bookings_slot_lookup ON one_to_one_bookings(offering_id, customer_name)`,
		`CREATE INDEX IF NOT EXISTS idx_one_to_one_bookings_coach ON one_to_one_bookings(coach_user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_one_to_one_bookings_status ON one_to_one_bookings(package_status)`,
		`CREATE INDEX IF NOT EXISTS idx_one_to_one_booking_sessions_booking ON one_to_one_booking_sessions(booking_id, session_number)`,
		`CREATE INDEX IF NOT EXISTS idx_one_to_one_booking_sessions_schedule ON one_to_one_booking_sessions(schedule_id)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_payment_collections_schedule ON booking_payment_collections(schedule_id, collected_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_payment_collections_voided ON booking_payment_collections(voided, schedule_id)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_request_changes_schedule ON booking_request_changes(schedule_id, changed_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_booking_communications_event_channel ON booking_communications(event_key, channel)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_communications_schedule ON booking_communications(schedule_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_communications_status ON booking_communications(status, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_communications_event_type ON booking_communications(event_type, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_communications_created_at ON booking_communications(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_access_tokens_schedule ON booking_access_tokens(schedule_id, active, expires_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_booking_access_tokens_public_id ON booking_access_tokens(public_id)`,
		`CREATE INDEX IF NOT EXISTS idx_booking_cancellation_requests_schedule ON booking_cancellation_requests(schedule_id, status, requested_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_booking_cancellation_requests_pending ON booking_cancellation_requests(schedule_id) WHERE status = 'pending'`,
		`ALTER TABLE events ADD COLUMN registration_deadline TEXT`,
		`ALTER TABLE events ADD COLUMN image_path TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE admissions ADD COLUMN student_id TEXT`,
		`ALTER TABLE admissions ADD COLUMN admission_date TEXT`,
		`ALTER TABLE admissions ADD COLUMN practice_type TEXT NOT NULL DEFAULT 'group_practice'`,
		`ALTER TABLE admissions ADD COLUMN payment_collected INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE admissions ADD COLUMN payment_collected_at DATETIME`,
		`ALTER TABLE admissions ADD COLUMN admission_payment_amount REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE admissions ADD COLUMN finance_transaction_id INTEGER`,
		`CREATE INDEX IF NOT EXISTS idx_training_programs_active
		ON training_programs(active)`,
		`CREATE INDEX IF NOT EXISTS idx_training_programs_sort_order
		ON training_programs(sort_order, name)`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil && !isIgnorableMigrationError(err, stmt) {
			return err
		}
	}
	if err := migrateDivisions(db); err != nil {
		return err
	}

	trainingProgramIDExists, err := tableHasColumn(
		db,
		"admissions",
		"training_program_id",
	)
	if err != nil {
		return fmt.Errorf(
			"check admissions training_program_id column: %w",
			err,
		)
	}

	if !trainingProgramIDExists {
		if _, err := db.Exec(`
			ALTER TABLE admissions
			ADD COLUMN training_program_id INTEGER
		`); err != nil {
			return fmt.Errorf(
				"add admissions training_program_id column: %w",
				err,
			)
		}
	}

	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_admissions_training_program_id
		ON admissions(training_program_id)
	`); err != nil {
		return fmt.Errorf(
			"create admissions training programme index: %w",
			err,
		)
	}

	if _, err := db.Exec(`UPDATE admissions SET student_id = 'STD-' || printf('%05d', id) WHERE student_id IS NULL OR TRIM(student_id) = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions
		SET admission_date = CASE
			WHEN created_at IS NOT NULL AND LENGTH(TRIM(CAST(created_at AS TEXT))) >= 10 THEN SUBSTR(TRIM(CAST(created_at AS TEXT)), 1, 10)
			ELSE ''
		END
		WHERE admission_date IS NULL OR TRIM(admission_date) = ''`); err != nil {
		return err
	}
	for _, migration := range []struct {
		table  string
		column string
		stmt   string
	}{
		{
			table:  "admissions",
			column: "free_admission",
			stmt:   `ALTER TABLE admissions ADD COLUMN free_admission INTEGER NOT NULL DEFAULT 0`,
		},
		{
			table:  "admissions",
			column: "free_monthly_fee",
			stmt:   `ALTER TABLE admissions ADD COLUMN free_monthly_fee INTEGER NOT NULL DEFAULT 0`,
		},
		{
			table:  "admissions",
			column: "photo_path",
			stmt:   `ALTER TABLE admissions ADD COLUMN photo_path TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "admissions",
			column: "qr_code_path",
			stmt:   `ALTER TABLE admissions ADD COLUMN qr_code_path TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "admissions",
			column: "qr_code_value",
			stmt:   `ALTER TABLE admissions ADD COLUMN qr_code_value TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "student_monthly_payments",
			column: "enrollment_id",
			stmt:   `ALTER TABLE student_monthly_payments ADD COLUMN enrollment_id INTEGER`,
		},
		{
			table:  "student_monthly_payments",
			column: "discount_amount",
			stmt:   `ALTER TABLE student_monthly_payments ADD COLUMN discount_amount REAL NOT NULL DEFAULT 0`,
		},
		{
			table:  "student_monthly_payments",
			column: "adjustment_reason",
			stmt:   `ALTER TABLE student_monthly_payments ADD COLUMN adjustment_reason TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "student_enrollments",
			column: "enrollment_date",
			stmt:   `ALTER TABLE student_enrollments ADD COLUMN enrollment_date TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "student_enrollments",
			column: "discounted_monthly_fee",
			stmt:   `ALTER TABLE student_enrollments ADD COLUMN discounted_monthly_fee REAL NOT NULL DEFAULT 0`,
		},
		{
			table:  "student_groups",
			column: "training_program_id",
			stmt:   `ALTER TABLE student_groups ADD COLUMN training_program_id INTEGER`,
		},
		{
			table:  "attendance_records",
			column: "session_id",
			stmt:   `ALTER TABLE attendance_records ADD COLUMN session_id INTEGER`,
		},
		{
			table:  "space_schedules",
			column: "customer_message",
			stmt:   `ALTER TABLE space_schedules ADD COLUMN customer_message TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "space_schedules",
			column: "status_changed_at",
			stmt:   `ALTER TABLE space_schedules ADD COLUMN status_changed_at DATETIME`,
		},
		{
			table:  "space_schedules",
			column: "status_changed_by_user_id",
			stmt:   `ALTER TABLE space_schedules ADD COLUMN status_changed_by_user_id INTEGER`,
		},
		{
			table:  "space_schedules",
			column: "status_change_source",
			stmt:   `ALTER TABLE space_schedules ADD COLUMN status_change_source TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "space_schedules",
			column: "cancellation_reason",
			stmt:   `ALTER TABLE space_schedules ADD COLUMN cancellation_reason TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "space_schedules",
			column: "cancellation_finance_note",
			stmt:   `ALTER TABLE space_schedules ADD COLUMN cancellation_finance_note TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "court_activities",
			column: "auto_accept",
			stmt:   `ALTER TABLE court_activities ADD COLUMN auto_accept INTEGER NOT NULL DEFAULT 0`,
		},
		{
			table:  "booking_request_changes",
			column: "customer_message",
			stmt:   `ALTER TABLE booking_request_changes ADD COLUMN customer_message TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "booking_request_changes",
			column: "previous_status",
			stmt:   `ALTER TABLE booking_request_changes ADD COLUMN previous_status TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "booking_request_changes",
			column: "new_status",
			stmt:   `ALTER TABLE booking_request_changes ADD COLUMN new_status TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "booking_request_changes",
			column: "change_source",
			stmt:   `ALTER TABLE booking_request_changes ADD COLUMN change_source TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "booking_request_changes",
			column: "finance_note",
			stmt:   `ALTER TABLE booking_request_changes ADD COLUMN finance_note TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "one_to_one_offerings",
			column: "occurrence",
			stmt:   `ALTER TABLE one_to_one_offerings ADD COLUMN occurrence TEXT NOT NULL DEFAULT 'per_day'`,
		},
		{
			table:  "one_to_one_offerings",
			column: "session_count",
			stmt:   `ALTER TABLE one_to_one_offerings ADD COLUMN session_count INTEGER NOT NULL DEFAULT 1`,
		},
		{
			table:  "one_to_one_bookings",
			column: "discounted_price",
			stmt:   `ALTER TABLE one_to_one_bookings ADD COLUMN discounted_price REAL NOT NULL DEFAULT -1`,
		},
		{
			table:  "one_to_one_bookings",
			column: "coach_fee",
			stmt:   `ALTER TABLE one_to_one_bookings ADD COLUMN coach_fee REAL NOT NULL DEFAULT 0`,
		},
		{
			table:  "one_to_one_bookings",
			column: "sessions",
			stmt:   `ALTER TABLE one_to_one_bookings ADD COLUMN sessions INTEGER NOT NULL DEFAULT 1`,
		},
		{
			table:  "one_to_one_bookings",
			column: "occurrence",
			stmt:   `ALTER TABLE one_to_one_bookings ADD COLUMN occurrence TEXT NOT NULL DEFAULT 'per_day'`,
		},
		{
			table:  "one_to_one_bookings",
			column: "max_sessions",
			stmt:   `ALTER TABLE one_to_one_bookings ADD COLUMN max_sessions INTEGER NOT NULL DEFAULT 1`,
		},
		{
			table:  "one_to_one_bookings",
			column: "coach_user_id",
			stmt:   `ALTER TABLE one_to_one_bookings ADD COLUMN coach_user_id INTEGER`,
		},
		{
			table:  "one_to_one_bookings",
			column: "package_status",
			stmt:   `ALTER TABLE one_to_one_bookings ADD COLUMN package_status TEXT NOT NULL DEFAULT 'active'`,
		},
		{
			table:  "one_to_one_bookings",
			column: "completed_sessions",
			stmt:   `ALTER TABLE one_to_one_bookings ADD COLUMN completed_sessions INTEGER NOT NULL DEFAULT 0`,
		},
		{
			table:  "one_to_one_bookings",
			column: "cancelled_sessions",
			stmt:   `ALTER TABLE one_to_one_bookings ADD COLUMN cancelled_sessions INTEGER NOT NULL DEFAULT 0`,
		},
		{
			table:  "one_to_one_booking_sessions",
			column: "coach_user_id",
			stmt:   `ALTER TABLE one_to_one_booking_sessions ADD COLUMN coach_user_id INTEGER`,
		},
		{
			table:  "one_to_one_booking_sessions",
			column: "coach_fee",
			stmt:   `ALTER TABLE one_to_one_booking_sessions ADD COLUMN coach_fee REAL NOT NULL DEFAULT 0`,
		},
		{
			table:  "one_to_one_booking_sessions",
			column: "attendance_status",
			stmt:   `ALTER TABLE one_to_one_booking_sessions ADD COLUMN attendance_status TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "one_to_one_booking_sessions",
			column: "attendance_note",
			stmt:   `ALTER TABLE one_to_one_booking_sessions ADD COLUMN attendance_note TEXT NOT NULL DEFAULT ''`,
		},
		{
			table:  "one_to_one_booking_sessions",
			column: "attendance_marked_at",
			stmt:   `ALTER TABLE one_to_one_booking_sessions ADD COLUMN attendance_marked_at DATETIME`,
		},
		{
			table:  "one_to_one_booking_sessions",
			column: "attendance_marked_by_user_id",
			stmt:   `ALTER TABLE one_to_one_booking_sessions ADD COLUMN attendance_marked_by_user_id INTEGER`,
		},
		{
			table:  "one_to_one_booking_sessions",
			column: "completed_at",
			stmt:   `ALTER TABLE one_to_one_booking_sessions ADD COLUMN completed_at DATETIME`,
		},
		{
			table:  "one_to_one_booking_sessions",
			column: "completed_by_user_id",
			stmt:   `ALTER TABLE one_to_one_booking_sessions ADD COLUMN completed_by_user_id INTEGER`,
		},
		{
			table:  "one_to_one_booking_sessions",
			column: "cancelled_at",
			stmt:   `ALTER TABLE one_to_one_booking_sessions ADD COLUMN cancelled_at DATETIME`,
		},
	} {
		exists, err := tableHasColumn(db, migration.table, migration.column)
		if err != nil {
			return fmt.Errorf("check %s %s column: %w", migration.table, migration.column, err)
		}
		if !exists {
			if _, err := db.Exec(migration.stmt); err != nil {
				return fmt.Errorf("add %s %s column: %w", migration.table, migration.column, err)
			}
		}
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS admission_training_programs (
			admission_id INTEGER NOT NULL,
			training_program_id INTEGER NOT NULL,
			created_at DATETIME NOT NULL,
			PRIMARY KEY (admission_id, training_program_id),
			FOREIGN KEY (admission_id) REFERENCES admissions(id) ON DELETE CASCADE,
			FOREIGN KEY (training_program_id) REFERENCES training_programs(id)
		)
	`); err != nil {
		return fmt.Errorf("create admission training programmes table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_admission_training_programs_admission_id ON admission_training_programs(admission_id)`); err != nil {
		return fmt.Errorf("create admission training programmes admission index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_admission_training_programs_program_id ON admission_training_programs(training_program_id)`); err != nil {
		return fmt.Errorf("create admission training programmes programme index: %w", err)
	}
	if _, err := db.Exec(`
		INSERT INTO admission_training_programs (
			admission_id,
			training_program_id,
			created_at
		)
		SELECT
			a.id,
			a.training_program_id,
			COALESCE(a.created_at, CURRENT_TIMESTAMP)
		FROM admissions a
		WHERE COALESCE(a.training_program_id, 0) > 0
			AND NOT EXISTS (
				SELECT 1
				FROM admission_training_programs atp
				WHERE atp.admission_id = a.id
					AND atp.training_program_id = a.training_program_id
			)
	`); err != nil {
		return fmt.Errorf("backfill admission training programmes: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS student_enrollments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			admission_id INTEGER NOT NULL,
			training_program_id INTEGER NOT NULL,
			enrollment_date TEXT NOT NULL DEFAULT '',
			free_admission INTEGER NOT NULL DEFAULT 0,
			free_monthly_fee INTEGER NOT NULL DEFAULT 0,
			discounted_monthly_fee REAL NOT NULL DEFAULT 0,
			payment_collected INTEGER NOT NULL DEFAULT 0,
			payment_collected_at DATETIME,
			admission_payment_amount REAL NOT NULL DEFAULT 0,
			finance_transaction_id INTEGER,
			active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(admission_id, training_program_id),
			FOREIGN KEY (admission_id) REFERENCES admissions(id) ON DELETE CASCADE,
			FOREIGN KEY (training_program_id) REFERENCES training_programs(id),
			FOREIGN KEY (finance_transaction_id) REFERENCES finance_transactions(id)
		)
	`); err != nil {
		return fmt.Errorf("create student enrollments table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_student_enrollments_admission_id ON student_enrollments(admission_id)`); err != nil {
		return fmt.Errorf("create student enrollments admission index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_student_enrollments_program_id ON student_enrollments(training_program_id)`); err != nil {
		return fmt.Errorf("create student enrollments programme index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_student_enrollments_active ON student_enrollments(active, admission_id)`); err != nil {
		return fmt.Errorf("create student enrollments active index: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS student_enrollment_leaves (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		enrollment_id INTEGER NOT NULL,
		start_date TEXT NOT NULL,
		end_date TEXT NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		active INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (enrollment_id) REFERENCES student_enrollments(id) ON DELETE CASCADE
	)`); err != nil {
		return fmt.Errorf("create student enrollment leaves table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_student_enrollment_leaves_enrollment_dates ON student_enrollment_leaves(enrollment_id, active, start_date, end_date)`); err != nil {
		return fmt.Errorf("create student enrollment leaves index: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS student_group_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id INTEGER NOT NULL,
		title TEXT NOT NULL,
		day_of_week TEXT NOT NULL,
		start_time TEXT NOT NULL,
		end_time TEXT NOT NULL,
		active INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (group_id) REFERENCES student_groups(id) ON DELETE CASCADE
	)`); err != nil {
		return fmt.Errorf("create student group sessions table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_student_groups_training_program_id ON student_groups(training_program_id)`); err != nil {
		return fmt.Errorf("create student groups training programme index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_student_group_sessions_group_id ON student_group_sessions(group_id, active, day_of_week, start_time)`); err != nil {
		return fmt.Errorf("create student group sessions index: %w", err)
	}
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_attendance_group_student_date`); err != nil {
		return fmt.Errorf("drop legacy attendance uniqueness index: %w", err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_attendance_group_session_student_date ON attendance_records(group_id, COALESCE(session_id, 0), admission_id, attendance_date)`); err != nil {
		return fmt.Errorf("create attendance session uniqueness index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_attendance_admission_month ON attendance_records(admission_id, attendance_date, status)`); err != nil {
		return fmt.Errorf("create attendance monthly index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_admissions_qr_code_value ON admissions(qr_code_value)`); err != nil {
		return fmt.Errorf("create admissions qr code index: %w", err)
	}
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_student_monthly_payment_student_month`); err != nil {
		return fmt.Errorf("drop legacy student monthly payment unique index: %w", err)
	}
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_student_monthly_payment_enrollment_month`); err != nil {
		return fmt.Errorf("drop enrollment student monthly payment unique index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_student_monthly_payments_admission_month ON student_monthly_payments(admission_id, payment_month, collected_at)`); err != nil {
		return fmt.Errorf("create student monthly payment admission index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_student_monthly_payments_enrollment_month ON student_monthly_payments(enrollment_id, payment_month, collected_at)`); err != nil {
		return fmt.Errorf("create student monthly payment enrollment index: %w", err)
	}
	if _, err := db.Exec(`
		INSERT INTO student_enrollments (
			admission_id,
			training_program_id,
			free_admission,
			free_monthly_fee,
			discounted_monthly_fee,
			payment_collected,
			payment_collected_at,
			admission_payment_amount,
			finance_transaction_id,
			active,
			created_at,
			updated_at
		)
		SELECT
			atp.admission_id,
			atp.training_program_id,
			CASE WHEN atp.training_program_id = a.training_program_id THEN COALESCE(a.free_admission, 0) ELSE 0 END,
			CASE WHEN atp.training_program_id = a.training_program_id THEN COALESCE(a.free_monthly_fee, 0) ELSE 0 END,
			0,
			CASE WHEN atp.training_program_id = a.training_program_id THEN COALESCE(a.payment_collected, 0) ELSE 0 END,
			CASE WHEN atp.training_program_id = a.training_program_id THEN a.payment_collected_at ELSE NULL END,
			CASE WHEN atp.training_program_id = a.training_program_id THEN COALESCE(a.admission_payment_amount, 0) ELSE 0 END,
			CASE WHEN atp.training_program_id = a.training_program_id THEN a.finance_transaction_id ELSE NULL END,
			1,
			COALESCE(a.created_at, CURRENT_TIMESTAMP),
			COALESCE(a.updated_at, COALESCE(a.created_at, CURRENT_TIMESTAMP))
		FROM admission_training_programs atp
		JOIN admissions a
			ON a.id = atp.admission_id
		WHERE NOT EXISTS (
			SELECT 1
			FROM student_enrollments se
			WHERE se.admission_id = atp.admission_id
				AND se.training_program_id = atp.training_program_id
		)
	`); err != nil {
		return fmt.Errorf("backfill student enrollments: %w", err)
	}
	if err := backfillStudentMonthlyPaymentEnrollments(db); err != nil {
		return fmt.Errorf("backfill student monthly payment enrollments: %w", err)
	}
	if _, err := db.Exec(`UPDATE admissions SET qr_code_value = student_id WHERE qr_code_value IS NULL OR TRIM(qr_code_value) = ''`); err != nil {
		return fmt.Errorf("backfill admission qr code values: %w", err)
	}
	if _, err := db.Exec(`UPDATE space_schedules SET status_change_source = '' WHERE status_change_source IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE space_schedules SET cancellation_reason = '' WHERE cancellation_reason IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE space_schedules SET cancellation_finance_note = '' WHERE cancellation_finance_note IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE booking_request_changes SET previous_status = '' WHERE previous_status IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE booking_request_changes SET new_status = '' WHERE new_status IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE booking_request_changes SET change_source = '' WHERE change_source IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE booking_request_changes SET finance_note = '' WHERE finance_note IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE booking_payment_collections SET payment_note = '' WHERE payment_note IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		INSERT INTO booking_payment_collections (
			schedule_id,
			finance_transaction_id,
			amount,
			payment_method,
			payment_note,
			collected_by_user_id,
			collected_at,
			created_at,
			voided,
			void_reason,
			voided_by_user_id,
			voided_at
		)
		SELECT
			bf.schedule_id,
			ft.id,
			ft.amount,
			CASE WHEN TRIM(COALESCE(ft.payment_method, '')) = '' THEN 'cash' ELSE ft.payment_method END,
			'',
			ft.recorded_by_user_id,
			ft.recorded_at,
			ft.created_at,
			0,
			'',
			NULL,
			NULL
		FROM booking_financials bf
		JOIN finance_transactions ft
			ON ft.id = bf.finance_transaction_id
		WHERE bf.finance_transaction_id IS NOT NULL
			AND bf.finance_transaction_id > 0
			AND NOT EXISTS (
				SELECT 1
				FROM booking_payment_collections bpc
				WHERE bpc.finance_transaction_id = ft.id
			)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		INSERT INTO booking_payment_collections (
			schedule_id,
			finance_transaction_id,
			amount,
			payment_method,
			payment_note,
			collected_by_user_id,
			collected_at,
			created_at,
			voided,
			void_reason,
			voided_by_user_id,
			voided_at
		)
		SELECT
			ft.reference_id,
			ft.id,
			ft.amount,
			CASE WHEN TRIM(COALESCE(ft.payment_method, '')) = '' THEN 'cash' ELSE ft.payment_method END,
			'',
			ft.recorded_by_user_id,
			ft.recorded_at,
			ft.created_at,
			0,
			'',
			NULL,
			NULL
		FROM finance_transactions ft
		WHERE ft.category = 'booking_payment'
			AND ft.reference_type = 'space_schedule'
			AND COALESCE(ft.reference_id, 0) > 0
			AND NOT EXISTS (
				SELECT 1
				FROM booking_payment_collections bpc
				WHERE bpc.finance_transaction_id = ft.id
			)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_space_schedules_status_changed_at ON space_schedules(status, status_changed_at DESC)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_booking_request_changes_status ON booking_request_changes(schedule_id, action_type, changed_at DESC)`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions SET practice_type = 'group_practice' WHERE practice_type IS NULL OR TRIM(practice_type) = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions SET payment_collected = 0 WHERE payment_collected IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions SET free_admission = 0 WHERE free_admission IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions SET free_monthly_fee = 0 WHERE free_monthly_fee IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE student_enrollments SET discounted_monthly_fee = 0 WHERE discounted_monthly_fee IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE admissions SET admission_payment_amount = 0 WHERE admission_payment_amount IS NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_admissions_admission_date ON admissions(admission_date)`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		INSERT INTO games (
			name,
			activity,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		)
		SELECT
			ca.display_name,
			ca.activity,
			'',
			COALESCE(ca.active, 1),
			COALESCE(ca.sort_order, 0),
			COALESCE(ca.created_at, CURRENT_TIMESTAMP),
			COALESCE(ca.updated_at, COALESCE(ca.created_at, CURRENT_TIMESTAMP))
		FROM court_activities ca
		WHERE ca.activity <> 'training'
			AND NOT EXISTS (
				SELECT 1
				FROM games g
				WHERE g.activity = ca.activity
			)
	`); err != nil {
		return fmt.Errorf("backfill games from court activities: %w", err)
	}
	for _, migration := range []struct {
		table  string
		column string
		stmt   string
	}{
		{table: "court_activities", column: "game_id", stmt: `ALTER TABLE court_activities ADD COLUMN game_id INTEGER NOT NULL DEFAULT 0`},
		{table: "pricing_rules", column: "game_id", stmt: `ALTER TABLE pricing_rules ADD COLUMN game_id INTEGER NOT NULL DEFAULT 0`},
		{table: "training_programs", column: "game_id", stmt: `ALTER TABLE training_programs ADD COLUMN game_id INTEGER NOT NULL DEFAULT 0`},
		{table: "events", column: "game_id", stmt: `ALTER TABLE events ADD COLUMN game_id INTEGER NOT NULL DEFAULT 0`},
	} {
		exists, err := tableHasColumn(db, migration.table, migration.column)
		if err != nil {
			return fmt.Errorf("check %s %s column: %w", migration.table, migration.column, err)
		}
		if !exists {
			if _, err := db.Exec(migration.stmt); err != nil {
				return fmt.Errorf("add %s %s column: %w", migration.table, migration.column, err)
			}
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_court_activities_game_id ON court_activities(game_id)`); err != nil {
		return fmt.Errorf("create court_activities game index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_pricing_rules_game_id ON pricing_rules(game_id)`); err != nil {
		return fmt.Errorf("create pricing_rules game index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_training_programs_game_id ON training_programs(game_id)`); err != nil {
		return fmt.Errorf("create training_programs game index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_events_game_id ON events(game_id)`); err != nil {
		return fmt.Errorf("create events game index: %w", err)
	}
	if _, err := db.Exec(`
		UPDATE court_activities
		SET game_id = COALESCE((SELECT g.id FROM games g WHERE g.activity = court_activities.activity), 0)
		WHERE game_id = 0 AND TRIM(activity) <> '' AND activity <> 'training'
	`); err != nil {
		return fmt.Errorf("backfill court activity games: %w", err)
	}
	if _, err := db.Exec(`
		UPDATE pricing_rules
		SET game_id = COALESCE((SELECT g.id FROM games g WHERE g.activity = pricing_rules.activity), 0)
		WHERE game_id = 0 AND TRIM(activity) <> ''
	`); err != nil {
		return fmt.Errorf("backfill pricing rule games: %w", err)
	}
	if _, err := db.Exec(`
		UPDATE training_programs
		SET game_id = COALESCE((SELECT g.id FROM games g WHERE g.activity = training_programs.activity), 0)
		WHERE game_id = 0 AND TRIM(activity) <> ''
	`); err != nil {
		return fmt.Errorf("backfill training program games: %w", err)
	}
	if err := migrateTrainingProgramUniqueness(db); err != nil {
		return err
	}
	if _, err := db.Exec(`
		UPDATE events
		SET game_id = COALESCE((
			SELECT g.id
			FROM games g
			WHERE g.name = events.category
			LIMIT 1
		), 0)
		WHERE game_id = 0 AND TRIM(category) <> ''
	`); err != nil {
		return fmt.Errorf("backfill event games by name: %w", err)
	}
	if _, err := db.Exec(`
		UPDATE events
		SET game_id = COALESCE((
			SELECT g.id
			FROM games g
			WHERE g.activity = events.category
			LIMIT 1
		), 0)
		WHERE game_id = 0 AND TRIM(category) <> ''
	`); err != nil {
		return fmt.Errorf("backfill event games by activity: %w", err)
	}
	for _, migration := range []struct {
		column string
		stmt   string
	}{
		{
			column: "coach_type",
			stmt:   `ALTER TABLE coach_profiles ADD COLUMN coach_type TEXT NOT NULL DEFAULT 'main'`,
		},
		{
			column: "parent_coach_id",
			stmt:   `ALTER TABLE coach_profiles ADD COLUMN parent_coach_id INTEGER`,
		},
	} {
		exists, err := tableHasColumn(db, "coach_profiles", migration.column)
		if err != nil {
			return fmt.Errorf("check coach_profiles %s column: %w", migration.column, err)
		}
		if !exists {
			if _, err := db.Exec(migration.stmt); err != nil {
				return fmt.Errorf("add coach_profiles %s column: %w", migration.column, err)
			}
		}
	}
	if _, err := db.Exec(`UPDATE coach_profiles SET coach_type = 'main' WHERE coach_type IS NULL OR TRIM(coach_type) = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE coach_profiles SET parent_coach_id = NULL WHERE coach_type = 'main'`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		UPDATE coach_profiles
		SET parent_coach_id = NULL
		WHERE parent_coach_id IS NOT NULL
			AND parent_coach_id NOT IN (SELECT id FROM users)
	`); err != nil {
		return err
	}
	attendanceRecordedByExists, err := tableHasColumn(
		db,
		"attendance_records",
		"recorded_by_user_id",
	)
	if err != nil {
		return fmt.Errorf(
			"check attendance recorded_by_user_id column: %w",
			err,
		)
	}

	if !attendanceRecordedByExists {
		if _, err := db.Exec(`
			ALTER TABLE attendance_records
			ADD COLUMN recorded_by_user_id INTEGER
		`); err != nil {
			return fmt.Errorf(
				"add attendance recorded_by_user_id column: %w",
				err,
			)
		}
	}

	admissionPricingMonthlyFeeExists, err := tableHasColumn(db, "admission_pricing", "monthly_fee")
	if err != nil {
		return err
	}
	if !admissionPricingMonthlyFeeExists {
		if _, err := db.Exec(`ALTER TABLE admission_pricing ADD COLUMN monthly_fee REAL NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE admission_pricing SET monthly_fee = 0 WHERE monthly_fee IS NULL`); err != nil {
		return err
	}
	referralCommissionExists, err := tableHasColumn(db, "pricing_settings", "referral_commission_amount")
	if err != nil {
		return err
	}
	if !referralCommissionExists {
		if _, err := db.Exec(`ALTER TABLE pricing_settings ADD COLUMN referral_commission_amount REAL NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE pricing_settings SET referral_commission_amount = 0 WHERE referral_commission_amount IS NULL`); err != nil {
		return err
	}

	bookingColumns := []struct {
		name       string
		definition string
	}{
		{name: "status", definition: "TEXT NOT NULL DEFAULT 'confirmed'"},
		{name: "requester_name", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "requester_email", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "requester_phone", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "requested_by_user_id", definition: "INTEGER"},
		{name: "review_note", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range bookingColumns {
		exists, err := tableHasColumn(db, "space_schedules", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE space_schedules ADD COLUMN %s %s", column.name, column.definition)); err != nil {
			return err
		}
	}
	statusExists, err := tableHasColumn(db, "space_schedules", "status")
	if err != nil {
		return err
	}
	if statusExists {
		if _, err := db.Exec(`UPDATE space_schedules SET status = 'confirmed' WHERE status IS NULL OR TRIM(status) = ''`); err != nil {
			return err
		}
		if _, err := db.Exec(`
			UPDATE space_schedules
			SET status = REPLACE(REPLACE(LOWER(TRIM(status)), '-', '_'), ' ', '_')
			WHERE status IS NOT NULL AND TRIM(status) <> ''
		`); err != nil {
			return err
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_space_schedules_status ON space_schedules(status)`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_admissions_student_id ON admissions(student_id)`); err != nil {
		return err
	}
	if err := seedAdmissionPricing(db); err != nil {
		return err
	}
	if err := seedPricingSettings(db); err != nil {
		return err
	}
	if err := backfillTrainingProgramDeliveryFormats(db); err != nil {
		return err
	}
	if err := backfillBookingFinancials(db); err != nil {
		return err
	}
	if err := migrateFinanceCashbook(db); err != nil {
		return err
	}
	if err := migrateMCPSQLiteSchema(db); err != nil {
		return err
	}

	if _, err := db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC()); err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM email_verifications WHERE expires_at <= ?`, time.Now().UTC())
	return err
}

func seedTrainingPrograms(db *sql.DB) error {
	now := time.Now().UTC()

	programs := []TrainingProgram{
		{
			Name:           "Group Practice - Cricket",
			Activity:       "cricket",
			TrainingFormat: "group",
			SortOrder:      30,
		},
		{
			Name:           "Group Practice - Zumba",
			Activity:       "zumba",
			TrainingFormat: "group",
			SortOrder:      40,
		},
		{
			Name:           "Group Practice - Badminton",
			Activity:       "badminton",
			TrainingFormat: "group",
			SortOrder:      60,
		},
	}

	for _, program := range programs {
		_, err := db.Exec(`
			INSERT INTO training_programs (
				name,
				activity,
				training_format,
				admission_fee,
				monthly_fee,
				active,
				sort_order,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, 0, 0, 1, ?, ?, ?)
			ON CONFLICT DO NOTHING
		`,
			program.Name,
			program.Activity,
			program.TrainingFormat,
			program.SortOrder,
			now,
			now,
		)
		if err != nil {
			return fmt.Errorf(
				"seed training programme %q: %w",
				program.Name,
				err,
			)
		}
	}

	return nil
}

func migrateTrainingProgramUniqueness(db *sql.DB) error {
	var tableSQL string
	if err := db.QueryRow(`
		SELECT COALESCE(sql, '')
		FROM sqlite_master
		WHERE type = 'table' AND name = 'training_programs'
	`).Scan(&tableSQL); err != nil {
		return fmt.Errorf("load training_programs schema: %w", err)
	}

	needsRebuild := strings.Contains(strings.ToUpper(tableSQL), "UNIQUE(ACTIVITY, TRAINING_FORMAT)")
	if !needsRebuild {
		if _, err := db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS idx_training_programs_division_name_ci
			ON training_programs(COALESCE(division_id, 0), LOWER(TRIM(name)))
		`); err != nil {
			return fmt.Errorf("create training programme division/name index: %w", err)
		}
		return nil
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for training programme rebuild: %w", err)
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`)

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin training programme rebuild: %w", err)
	}
	defer tx.Rollback()

	statements := []string{
		`DROP TABLE IF EXISTS training_programs_rebuilt`,
		`CREATE TABLE training_programs_rebuilt (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			activity TEXT NOT NULL,
			training_format TEXT NOT NULL,
			admission_fee REAL NOT NULL DEFAULT 0,
			monthly_fee REAL NOT NULL DEFAULT 0,
			active INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			division_id INTEGER,
			game_id INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO training_programs_rebuilt (
			id,
			name,
			activity,
			training_format,
			admission_fee,
			monthly_fee,
			active,
			sort_order,
			created_at,
			updated_at,
			division_id,
			game_id
		)
		SELECT
			id,
			name,
			activity,
			training_format,
			admission_fee,
			monthly_fee,
			active,
			sort_order,
			created_at,
			updated_at,
			division_id,
			game_id
		FROM training_programs`,
		`DROP TABLE training_programs`,
		`ALTER TABLE training_programs_rebuilt RENAME TO training_programs`,
		`CREATE UNIQUE INDEX idx_training_programs_division_name_ci
			ON training_programs(COALESCE(division_id, 0), LOWER(TRIM(name)))`,
	}

	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("rebuild training_programmes uniqueness: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit training programme uniqueness rebuild: %w", err)
	}

	return nil
}

func backfillTrainingProgramDeliveryFormats(db *sql.DB) error {
	var salaryProfileTableCount int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table'
		  AND name = 'staff_salary_profiles'
	`).Scan(&salaryProfileTableCount); err != nil {
		return fmt.Errorf("check staff_salary_profiles table: %w", err)
	}
	hasSalaryProfiles := salaryProfileTableCount > 0

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT id, activity
		FROM training_programs
		WHERE training_format = 'one_to_one'
		ORDER BY id ASC
	`)
	if err != nil {
		return fmt.Errorf("load legacy one-to-one training programmes: %w", err)
	}
	defer rows.Close()

	type legacyProgram struct {
		ID       int64
		Activity string
	}

	legacyPrograms := make([]legacyProgram, 0)
	for rows.Next() {
		var program legacyProgram
		if err := rows.Scan(&program.ID, &program.Activity); err != nil {
			return fmt.Errorf("scan legacy one-to-one training programme: %w", err)
		}
		legacyPrograms = append(legacyPrograms, program)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy one-to-one training programmes: %w", err)
	}

	now := time.Now().UTC()

	for _, legacy := range legacyPrograms {
		var keeperID int64
		err := tx.QueryRow(`
			SELECT id
			FROM training_programs
			WHERE activity = ?
			  AND training_format = 'group'
			  AND id <> ?
			ORDER BY id ASC
			LIMIT 1
		`, legacy.Activity, legacy.ID).Scan(&keeperID)

		switch {
		case err == sql.ErrNoRows:
			if _, err := tx.Exec(`
				UPDATE training_programs
				SET training_format = 'group',
				    updated_at = ?
				WHERE id = ?
			`, now, legacy.ID); err != nil {
				return fmt.Errorf("convert legacy training programme %d to group: %w", legacy.ID, err)
			}

		case err != nil:
			return fmt.Errorf("find group keeper for training programme %d: %w", legacy.ID, err)

		default:
			if _, err := tx.Exec(`
				UPDATE admissions
				SET training_program_id = ?
				WHERE training_program_id = ?
			`, keeperID, legacy.ID); err != nil {
				return fmt.Errorf("repoint admissions from training programme %d: %w", legacy.ID, err)
			}

			if _, err := tx.Exec(`
				UPDATE student_groups
				SET training_program_id = ?
				WHERE training_program_id = ?
			`, keeperID, legacy.ID); err != nil {
				return fmt.Errorf("repoint student groups from training programme %d: %w", legacy.ID, err)
			}

			if hasSalaryProfiles {
				if _, err := tx.Exec(`
					UPDATE staff_salary_profiles
					SET training_program_id = ?
					WHERE training_program_id = ?
				`, keeperID, legacy.ID); err != nil {
					return fmt.Errorf("repoint staff salary profiles from training programme %d: %w", legacy.ID, err)
				}
			}

			if _, err := tx.Exec(`
				UPDATE admission_training_programs
				SET training_program_id = ?
				WHERE training_program_id = ?
				  AND NOT EXISTS (
					SELECT 1
					FROM admission_training_programs existing
					WHERE existing.admission_id = admission_training_programs.admission_id
					  AND existing.training_program_id = ?
				  )
			`, keeperID, legacy.ID, keeperID); err != nil {
				return fmt.Errorf("repoint admission training programmes from training programme %d: %w", legacy.ID, err)
			}

			if _, err := tx.Exec(`
				DELETE FROM admission_training_programs
				WHERE training_program_id = ?
			`, legacy.ID); err != nil {
				return fmt.Errorf("delete duplicate admission training programmes for %d: %w", legacy.ID, err)
			}

			if _, err := tx.Exec(`
				UPDATE student_enrollments
				SET training_program_id = ?
				WHERE training_program_id = ?
				  AND NOT EXISTS (
					SELECT 1
					FROM student_enrollments existing
					WHERE existing.admission_id = student_enrollments.admission_id
					  AND existing.training_program_id = ?
				  )
			`, keeperID, legacy.ID, keeperID); err != nil {
				return fmt.Errorf("repoint student enrollments from training programme %d: %w", legacy.ID, err)
			}

			if _, err := tx.Exec(`
				DELETE FROM student_enrollments
				WHERE training_program_id = ?
			`, legacy.ID); err != nil {
				return fmt.Errorf("delete duplicate student enrollments for %d: %w", legacy.ID, err)
			}

			if _, err := tx.Exec(`
				DELETE FROM training_programs
				WHERE id = ?
			`, legacy.ID); err != nil {
				return fmt.Errorf("delete obsolete one-to-one training programme %d: %w", legacy.ID, err)
			}
		}
	}

	if _, err := tx.Exec(`
		UPDATE training_programs
		SET training_format = 'group',
		    updated_at = ?
		WHERE training_format IS NULL
		   OR TRIM(training_format) = ''
		   OR training_format <> 'group'
	`, now); err != nil {
		return fmt.Errorf("finalize group-only training programme formats: %w", err)
	}

	return tx.Commit()
}

func backfillBookingFinancials(db *sql.DB) error {
	rows, err := db.Query(`
		SELECT s.id, s.slot_date, s.slot_hour, s.activity, s.quantity,
		       CASE
		         WHEN CAST(strftime('%w', s.slot_date) AS INTEGER) IN (0, 6) THEN
		           CASE WHEN s.slot_hour BETWEEN ps.peak_start_hour AND ps.peak_end_hour
		                THEN pr.weekend_peak_price ELSE pr.weekend_offpeak_price END
		         ELSE
		           CASE WHEN s.slot_hour BETWEEN ps.peak_start_hour AND ps.peak_end_hour
		                THEN pr.weekday_peak_price ELSE pr.weekday_offpeak_price END
		       END AS quoted_amount
		FROM space_schedules s
		JOIN pricing_rules pr ON pr.activity = s.activity AND pr.quantity = s.quantity
		JOIN pricing_settings ps ON ps.id = 1
		LEFT JOIN booking_financials bf ON bf.schedule_id = s.id
		WHERE s.entry_type = 'booking' AND bf.id IS NULL
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		scheduleID int64
		amount     float64
	}
	var missing []row
	for rows.Next() {
		var item row
		var slotDate, slotHour, activity string
		var quantity int
		if err := rows.Scan(&item.scheduleID, &slotDate, &slotHour, &activity, &quantity, &item.amount); err != nil {
			return err
		}
		missing = append(missing, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, item := range missing {
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO booking_financials (
				schedule_id, quoted_amount, paid, payment_method, created_at, updated_at
			) VALUES (?, ?, 0, '', ?, ?)
		`, item.scheduleID, item.amount, now, now); err != nil {
			return err
		}
	}
	return nil
}

func backfillStudentMonthlyPaymentEnrollments(db *sql.DB) error {
	rows, err := db.Query(`
		SELECT id, admission_id
		FROM student_monthly_payments
		WHERE enrollment_id IS NULL
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type paymentRecord struct {
		paymentID   int64
		admissionID int64
	}

	var payments []paymentRecord
	for rows.Next() {
		var payment paymentRecord
		if err := rows.Scan(&payment.paymentID, &payment.admissionID); err != nil {
			return err
		}
		payments = append(payments, payment)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, payment := range payments {
		var enrollmentID int64
		err := db.QueryRow(`
			SELECT se.id
			FROM student_enrollments se
			LEFT JOIN admissions a
				ON a.id = se.admission_id
			WHERE se.admission_id = ?
			ORDER BY
				CASE WHEN se.training_program_id = COALESCE(a.training_program_id, 0) THEN 0 ELSE 1 END,
				se.id
			LIMIT 1
		`, payment.admissionID).Scan(&enrollmentID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return err
		}
		if _, err := db.Exec(`UPDATE student_monthly_payments SET enrollment_id = ? WHERE id = ?`, enrollmentID, payment.paymentID); err != nil {
			return err
		}
	}

	return nil
}

func tableHasColumn(db *sql.DB, tableName, columnName string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err == nil {
		defer rows.Close()

		for rows.Next() {
			var (
				cid        int
				name       string
				columnType string
				notNull    int
				defaultVal sql.NullString
				pk         int
			)
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
				return false, err
			}
			if name == columnName {
				return true, nil
			}
		}
		return false, rows.Err()
	}

	var exists bool
	if queryErr := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = $1
			  AND column_name = $2
		)
	`, tableName, columnName).Scan(&exists); queryErr != nil {
		return false, err
	}
	return exists, nil
}

func seedRoles(db *sql.DB) error {
	for _, role := range allRoles {
		if _, err := db.Exec(`INSERT OR IGNORE INTO roles (name) VALUES (?)`, role); err != nil {
			return err
		}
	}
	rolePermissions := map[string][]string{
		"customer": {"dashboard.view"},
		"editor":   {"dashboard.view", "editor.access"},
		"coach":    {"dashboard.view", "attendance.manage"},
		"admin": {
			"dashboard.view",
			"editor.access",
			"users.manage",
			"roles.manage",
			"user_divisions.manage",
			"admissions.manage",
			"coaches.manage",
			"training_programs.manage",
			"student_groups.manage",
			"attendance.manage",
			"space_bookings.manage",
			"booking_requests.manage",
			"pricing.manage",
			"finance.manage",
			"reports.view",
			"events.manage",
		},
		"superadmin": allPermissions,
	}
	for roleName, permissions := range rolePermissions {
		roleID, err := queryRoleID(db, roleName)
		if err != nil {
			return err
		}
		for _, permission := range permissions {
			if _, err := db.Exec(`INSERT OR IGNORE INTO role_permissions (role_id, permission) VALUES (?, ?)`, roleID, permission); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyCourtManagerConfiguration(db *sql.DB) error {
	var activeCourtCount int

	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM courts
		WHERE active = 1
	`).Scan(&activeCourtCount)
	if err != nil {
		return err
	}

	if activeCourtCount == 0 {
		return errors.New("court manager has no active courts")
	}

	var activeLayoutCount int

	err = db.QueryRow(`
		SELECT COUNT(*)
		FROM court_layouts cl
		JOIN courts c
			ON c.id = cl.court_id
		WHERE cl.active = 1
		  AND c.active = 1
	`).Scan(&activeLayoutCount)
	if err != nil {
		return err
	}

	if activeLayoutCount == 0 {
		return errors.New("court manager has no active court layouts")
	}

	var emptyLayoutCount int

	err = db.QueryRow(`
		SELECT COUNT(*)
		FROM court_layouts cl
		WHERE cl.active = 1
		AND NOT EXISTS (
			SELECT 1
			FROM court_layout_items cli
			WHERE cli.layout_id = cl.id
		)
	`).Scan(&emptyLayoutCount)
	if err != nil {
		return err
	}

	if emptyLayoutCount > 0 {
		return errors.New("court manager contains an empty active layout")
	}

	return nil
}

func seedCourtManager(db *sql.DB) error {
	var existingCourtCount int

	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM courts
	`).Scan(&existingCourtCount); err != nil {
		return fmt.Errorf(
			"count existing courts before seed: %w",
			err,
		)
	}

	if existingCourtCount > 0 {
		return nil
	}
	tx, err := db.Begin()

	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	_, err = tx.Exec(`
		INSERT OR IGNORE INTO courts (
			name,
			code,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, 1, 10, ?, ?)
	`,
		"Main Indoor Court",
		"MAIN_INDOOR",
		"Shared multipurpose indoor court used by badminton, cricket nets, table tennis, futsal, tennis, indoor cricket, and training.",
		now,
		now,
	)
	if err != nil {
		return err
	}

	var courtID int64
	err = tx.QueryRow(`
		SELECT id
		FROM courts
		WHERE code = ?
	`, "MAIN_INDOOR").Scan(&courtID)
	if err != nil {
		return err
	}

	activities := []struct {
		Activity    string
		DisplayName string
		MaxQuantity int
		AutoAccept  bool
		SortOrder   int
	}{
		{
			Activity:    "full_indoor_cricket",
			DisplayName: "Full Indoor Cricket",
			MaxQuantity: 1,
			AutoAccept:  false,
			SortOrder:   10,
		},
		{
			Activity:    "futsal",
			DisplayName: "Futsal",
			MaxQuantity: 1,
			AutoAccept:  false,
			SortOrder:   20,
		},
		{
			Activity:    "badminton",
			DisplayName: "Badminton",
			MaxQuantity: 1,
			AutoAccept:  false,
			SortOrder:   30,
		},
		{
			Activity:    "cricket_net",
			DisplayName: "Cricket Net",
			MaxQuantity: 3,
			AutoAccept:  false,
			SortOrder:   40,
		},
		{
			Activity:    "table_tennis",
			DisplayName: "Table Tennis",
			MaxQuantity: 2,
			AutoAccept:  false,
			SortOrder:   50,
		},
		{
			Activity:    "tennis",
			DisplayName: "Tennis",
			MaxQuantity: 1,
			AutoAccept:  false,
			SortOrder:   60,
		},
		{
			Activity:    "training",
			DisplayName: "Training Session",
			MaxQuantity: 1,
			AutoAccept:  false,
			SortOrder:   70,
		},
	}

	for _, activity := range activities {
		_, err = tx.Exec(`
			INSERT OR IGNORE INTO court_activities (
				court_id,
				activity,
				display_name,
				max_quantity,
				auto_accept,
				active,
				sort_order,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)
		`,
			courtID,
			activity.Activity,
			activity.DisplayName,
			activity.MaxQuantity,
			boolToInt(activity.AutoAccept),
			activity.SortOrder,
			now,
			now,
		)
		if err != nil {
			return err
		}
	}

	type seedLayoutItem struct {
		Activity string
		Quantity int
	}

	type seedLayout struct {
		Name        string
		Description string
		SortOrder   int
		Items       []seedLayoutItem
	}

	layouts := []seedLayout{
		{
			Name:        "Full Indoor Cricket",
			Description: "Full-court indoor cricket configuration.",
			SortOrder:   10,
			Items: []seedLayoutItem{
				{Activity: "full_indoor_cricket", Quantity: 1},
			},
		},
		{
			Name:        "Futsal",
			Description: "Full-court futsal configuration.",
			SortOrder:   20,
			Items: []seedLayoutItem{
				{Activity: "futsal", Quantity: 1},
			},
		},
		{
			Name:        "Badminton and Cricket Net",
			Description: "One badminton booking and one cricket-net booking may operate simultaneously.",
			SortOrder:   30,
			Items: []seedLayoutItem{
				{Activity: "badminton", Quantity: 1},
				{Activity: "cricket_net", Quantity: 1},
			},
		},
		{
			Name:        "Badminton and Table Tennis",
			Description: "One badminton booking and one table-tennis booking may operate simultaneously.",
			SortOrder:   40,
			Items: []seedLayoutItem{
				{Activity: "badminton", Quantity: 1},
				{Activity: "table_tennis", Quantity: 1},
			},
		},
		{
			Name:        "Three Cricket Nets",
			Description: "Up to three cricket nets may operate simultaneously.",
			SortOrder:   50,
			Items: []seedLayoutItem{
				{Activity: "cricket_net", Quantity: 3},
			},
		},
		{
			Name:        "Two Table Tennis Tables",
			Description: "Up to two table-tennis bookings may operate simultaneously.",
			SortOrder:   60,
			Items: []seedLayoutItem{
				{Activity: "table_tennis", Quantity: 2},
			},
		},
		{
			Name:        "Tennis",
			Description: "Full-court tennis configuration.",
			SortOrder:   70,
			Items: []seedLayoutItem{
				{Activity: "tennis", Quantity: 1},
			},
		},
		{
			Name:        "Training Session",
			Description: "Training session that reserves the complete configured court.",
			SortOrder:   80,
			Items: []seedLayoutItem{
				{Activity: "training", Quantity: 1},
			},
		},
	}

	for _, layout := range layouts {
		_, err = tx.Exec(`
			INSERT OR IGNORE INTO court_layouts (
				court_id,
				name,
				description,
				active,
				sort_order,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, 1, ?, ?, ?)
		`,
			courtID,
			layout.Name,
			layout.Description,
			layout.SortOrder,
			now,
			now,
		)
		if err != nil {
			return err
		}

		var layoutID int64
		err = tx.QueryRow(`
			SELECT id
			FROM court_layouts
			WHERE court_id = ?
			  AND name = ?
		`,
			courtID,
			layout.Name,
		).Scan(&layoutID)
		if err != nil {
			return err
		}

		for _, item := range layout.Items {
			_, err = tx.Exec(`
				INSERT OR IGNORE INTO court_layout_items (
					layout_id,
					activity,
					quantity
				)
				VALUES (?, ?, ?)
			`,
				layoutID,
				item.Activity,
				item.Quantity,
			)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (a *App) activeBookingConfiguration() (
	[]CourtActivity,
	[]CourtLayout,
	error,
) {
	return activeBookingConfigurationQuery(a.db)
}

func seedPricingRules(db *sql.DB) error {
	for _, option := range defaultBookingOptionCatalog() {
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO pricing_rules (
				activity, quantity, weekday_offpeak_price, weekday_peak_price,
				weekend_offpeak_price, weekend_peak_price, created_at, updated_at
			)
			VALUES (?, ?, 0, 0, 0, 0, ?, ?)
		`, option.Activity, option.Quantity, time.Now().UTC(), time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func seedAdmissionPricing(db *sql.DB) error {
	now := time.Now().UTC()
	defaults := []AdmissionPricing{
		{PracticeType: "group_practice", Price: 0, MonthlyFee: 0},
		{PracticeType: "one_to_one_practice", Price: 0, MonthlyFee: 0},
	}
	for _, pricing := range defaults {
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO admission_pricing (practice_type, price, monthly_fee, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
		`, pricing.PracticeType, pricing.Price, pricing.MonthlyFee, now, now); err != nil {
			return err
		}
	}
	return nil
}

func seedPricingSettings(db *sql.DB) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO pricing_settings (id, peak_start_hour, peak_end_hour, created_at, updated_at)
		VALUES (1, '17:00', '23:00', ?, ?)
	`, time.Now().UTC(), time.Now().UTC())
	return err
}

func roleIDByName(tx *sql.Tx, role string) (int64, error) {
	row := tx.QueryRow(`SELECT id FROM roles WHERE name = ?`, role)
	var roleID int64
	if err := row.Scan(&roleID); err != nil {
		return 0, err
	}
	return roleID, nil
}

func queryRoleID(db *sql.DB, role string) (int64, error) {
	row := db.QueryRow(`SELECT id FROM roles WHERE name = ?`, role)
	var roleID int64
	if err := row.Scan(&roleID); err != nil {
		return 0, err
	}
	return roleID, nil
}

func generateToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func generateOTP() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func (a *App) issueVerificationCode(userID int64) (string, error) {
	otp, err := generateOTP()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	_, err = a.db.Exec(`
		INSERT INTO email_verifications (user_id, otp_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			otp_hash = excluded.otp_hash,
			expires_at = excluded.expires_at,
			created_at = excluded.created_at
	`, userID, hashValue(otp), now.Add(otpTTL), now)
	if err != nil {
		return "", err
	}
	return otp, nil
}

func (a *App) sendVerificationEmail(user *User, otp string) error {
	body := fmt.Sprintf(
		"Hi %s,\r\n\r\nYour mekmaa3 verification code is %s.\r\nIt expires in 10 minutes.\r\n\r\nIf you did not create this account, you can ignore this email.\r\n",
		user.Name,
		otp,
	)
	return a.sendEmailMessage(user.Email, "Verify your email address", body, "")
}

func (a *App) sendBookingConfirmationSMS(schedule *SpaceSchedule) error {
	if schedule == nil {
		return errors.New("schedule is required")
	}
	return a.sendSMSMessage(schedule.RequesterPhone, buildBookingConfirmationSMSBody(schedule))
}

func (a *App) consumeVerificationCode(userID int64, otp string) error {
	row := a.db.QueryRow(`SELECT otp_hash, expires_at FROM email_verifications WHERE user_id = ?`, userID)
	var otpHash string
	var expiresAt time.Time
	if err := row.Scan(&otpHash, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidOTP
		}
		return err
	}

	if expiresAt.Before(time.Now().UTC()) || otpHash != hashValue(otp) {
		return ErrInvalidOTP
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE users SET email_verified_at = ? WHERE id = ?`, time.Now().UTC(), userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM email_verifications WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}
