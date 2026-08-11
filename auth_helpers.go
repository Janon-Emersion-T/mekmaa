package main

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func currentUserID(r *http.Request) int64 {
	user, _ := currentUserFromRequest(r)
	if user == nil {
		return 0
	}
	return user.ID
}

func currentUserFromRequest(r *http.Request) (*User, bool) {
	if r == nil {
		return nil, false
	}
	user, ok := r.Context().Value(userContextKey).(*User)
	return user, ok
}

func userHasAnyRole(user *User, roles ...string) bool {
	if user == nil {
		return false
	}
	for _, candidate := range roles {
		for _, assigned := range user.Roles {
			if assigned == candidate {
				return true
			}
		}
	}
	return false
}

func userHasRole(user *User, role string) bool {
	return userHasAnyRole(user, role)
}

func containsRole(roles []string, target string) bool {
	for _, role := range roles {
		if role == target {
			return true
		}
	}
	return false
}

func containsPrivilegedRole(roles []string) bool {
	for _, role := range roles {
		if isPrivilegedRole(role) {
			return true
		}
	}
	return false
}

func normalizeRoleNames(roles []string) []string {
	seen := map[string]struct{}{}
	var normalized []string
	for _, role := range roles {
		role = normalizeRoleName(role)
		if role == "" {
			continue
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		normalized = append(normalized, role)
	}
	sort.Strings(normalized)
	return normalized
}

func (a *App) normalizeExistingRoles(roles []string) ([]string, error) {
	normalized := normalizeRoleNames(roles)
	if len(normalized) == 0 {
		return nil, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(normalized)), ",")
	args := make([]any, len(normalized))
	for i, role := range normalized {
		args[i] = role
	}
	rows, err := a.db.Query(`SELECT name FROM roles WHERE name IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	existing := make(map[string]struct{}, len(normalized))
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		existing[role] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(existing) != len(normalized) {
		return nil, errors.New("unknown role")
	}
	return normalized, nil
}

func normalizePermissions(permissions []string) []string {
	allowed := make(map[string]struct{}, len(allPermissions))
	for _, permission := range allPermissions {
		allowed[permission] = struct{}{}
	}

	seen := map[string]struct{}{}
	var normalized []string
	for _, permission := range permissions {
		permission = strings.ToLower(strings.TrimSpace(permission))
		if _, ok := allowed[permission]; !ok {
			continue
		}
		if _, ok := seen[permission]; ok {
			continue
		}
		seen[permission] = struct{}{}
		normalized = append(normalized, permission)
	}
	sort.Strings(normalized)
	return normalized
}

func containsSensitivePermission(permissions []string) bool {
	return containsPermission(permissions, "users.manage") || containsPermission(permissions, "roles.manage")
}

func normalizePositiveIDs(values []string) []int64 {
	seen := map[int64]struct{}{}
	var ids []int64
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
