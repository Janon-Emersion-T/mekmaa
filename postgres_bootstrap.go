package main

import (
	"database/sql"
	"fmt"
	"time"
)

func applyPostgresBootstrapData(db *sql.DB) error {
	if err := seedPostgresRoles(db); err != nil {
		return fmt.Errorf("seed PostgreSQL roles: %w", err)
	}

	if err := seedPostgresCoreBookingData(db); err != nil {
		return fmt.Errorf("seed PostgreSQL booking configuration: %w", err)
	}

	return nil
}

func seedPostgresRoles(db *sql.DB) error {
	for _, role := range allRoles {
		if _, err := db.Exec(`
			INSERT INTO roles (name)
			VALUES ($1)
			ON CONFLICT (name) DO NOTHING
		`, role); err != nil {
			return err
		}
	}

	rolePermissions := map[string][]string{
		"customer": {
			"dashboard.view",
		},
		"editor": {
			"dashboard.view",
			"editor.access",
		},
		"coach": {
			"dashboard.view",
			"attendance.manage",
		},
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
		var roleID int64

		if err := db.QueryRow(`
			SELECT id
			FROM roles
			WHERE name = $1
		`, roleName).Scan(&roleID); err != nil {
			return err
		}

		for _, permission := range permissions {
			if _, err := db.Exec(`
				INSERT INTO role_permissions (
					role_id,
					permission
				)
				VALUES ($1, $2)
				ON CONFLICT (role_id, permission) DO NOTHING
			`, roleID, permission); err != nil {
				return err
			}
		}
	}

	return nil
}

func seedPostgresCoreBookingData(db *sql.DB) error {
	now := time.Now().UTC()

	var courtID int64

	err := db.QueryRow(`
		INSERT INTO courts (
			name,
			code,
			description,
			active,
			sort_order,
			created_at,
			updated_at
		)
		VALUES (
			'Main Indoor Court',
			'MAIN_INDOOR',
			'Shared multipurpose indoor court used by badminton, cricket nets, table tennis, futsal, tennis, indoor cricket, and training.',
			1,
			10,
			$1,
			$1
		)
		ON CONFLICT (code)
		DO UPDATE SET
			name = EXCLUDED.name
		RETURNING id
	`, now).Scan(&courtID)

	if err != nil {
		return err
	}

	type activitySeed struct {
		Activity    string
		DisplayName string
		MaxQuantity int
		SortOrder   int
	}

	activities := []activitySeed{
		{"full_indoor_cricket", "Full Indoor Cricket", 1, 10},
		{"futsal", "Futsal", 1, 20},
		{"badminton", "Badminton", 1, 30},
		{"cricket_net", "Cricket Net", 3, 40},
		{"table_tennis", "Table Tennis", 2, 50},
		{"tennis", "Tennis", 1, 60},
		{"training", "Training Session", 1, 70},
	}

	for _, item := range activities {
		if _, err := db.Exec(`
			INSERT INTO court_activities (
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
			VALUES (
				$1, $2, $3, $4,
				0, 1, $5, $6, $6
			)
			ON CONFLICT (court_id, activity)
			DO UPDATE SET
				display_name = EXCLUDED.display_name,
				max_quantity = EXCLUDED.max_quantity,
				sort_order = EXCLUDED.sort_order
		`,
			courtID,
			item.Activity,
			item.DisplayName,
			item.MaxQuantity,
			item.SortOrder,
			now,
		); err != nil {
			return err
		}
	}

	type layoutSeed struct {
		Name        string
		Description string
		SortOrder   int
		Items       map[string]int
	}

	layouts := []layoutSeed{
		{
			"Full Indoor Cricket",
			"Full-court indoor cricket configuration.",
			10,
			map[string]int{"full_indoor_cricket": 1},
		},
		{
			"Futsal",
			"Full-court futsal configuration.",
			20,
			map[string]int{"futsal": 1},
		},
		{
			"Badminton and Cricket Net",
			"One badminton booking and one cricket-net booking may operate simultaneously.",
			30,
			map[string]int{
				"badminton":   1,
				"cricket_net": 1,
			},
		},
		{
			"Badminton and Table Tennis",
			"One badminton booking and one table-tennis booking may operate simultaneously.",
			40,
			map[string]int{
				"badminton":    1,
				"table_tennis": 1,
			},
		},
		{
			"Three Cricket Nets",
			"Up to three cricket nets may operate simultaneously.",
			50,
			map[string]int{"cricket_net": 3},
		},
		{
			"Two Table Tennis Tables",
			"Up to two table-tennis bookings may operate simultaneously.",
			60,
			map[string]int{"table_tennis": 2},
		},
		{
			"Tennis",
			"Full-court tennis configuration.",
			70,
			map[string]int{"tennis": 1},
		},
		{
			"Training Session",
			"Training session that reserves the complete configured court.",
			80,
			map[string]int{"training": 1},
		},
	}

	for _, layout := range layouts {
		var layoutID int64

		err := db.QueryRow(`
			INSERT INTO court_layouts (
				court_id,
				name,
				description,
				active,
				sort_order,
				created_at,
				updated_at
			)
			VALUES (
				$1, $2, $3, 1, $4, $5, $5
			)
			ON CONFLICT (court_id, name)
			DO UPDATE SET
				description = EXCLUDED.description,
				sort_order = EXCLUDED.sort_order
			RETURNING id
		`,
			courtID,
			layout.Name,
			layout.Description,
			layout.SortOrder,
			now,
		).Scan(&layoutID)

		if err != nil {
			return err
		}

		for activity, quantity := range layout.Items {
			if _, err := db.Exec(`
				INSERT INTO court_layout_items (
					layout_id,
					activity,
					quantity
				)
				VALUES ($1, $2, $3)
				ON CONFLICT (layout_id, activity)
				DO UPDATE SET
					quantity = EXCLUDED.quantity
			`,
				layoutID,
				activity,
				quantity,
			); err != nil {
				return err
			}
		}
	}

	bookingOptions := defaultBookingOptionCatalog()

	for _, option := range bookingOptions {
		if _, err := db.Exec(`
			INSERT INTO pricing_rules (
				activity,
				quantity,
				weekday_offpeak_price,
				weekday_peak_price,
				weekend_offpeak_price,
				weekend_peak_price,
				created_at,
				updated_at
			)
			VALUES (
				$1, $2,
				0, 0, 0, 0,
				$3, $3
			)
			ON CONFLICT (activity, quantity)
			DO NOTHING
		`,
			option.Activity,
			option.Quantity,
			now,
		); err != nil {
			return err
		}
	}

	if _, err := db.Exec(`
		INSERT INTO admission_pricing (
			practice_type,
			price,
			monthly_fee,
			created_at,
			updated_at
		)
		VALUES
			('group_practice', 0, 0, $1, $1),
			('one_to_one_practice', 0, 0, $1, $1)
		ON CONFLICT (practice_type)
		DO NOTHING
	`, now); err != nil {
		return err
	}

	if _, err := db.Exec(`
		INSERT INTO pricing_settings (
			id,
			peak_start_hour,
			peak_end_hour,
			referral_commission_amount,
			created_at,
			updated_at
		)
		VALUES (
			1,
			'17:00',
			'23:00',
			0,
			$1,
			$1
		)
		ON CONFLICT (id)
		DO NOTHING
	`, now); err != nil {
		return err
	}

	return nil
}
