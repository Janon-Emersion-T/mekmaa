package main

import (
	"fmt"
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

func (a *App) normalizeExistingRoles(
	values []string,
) ([]string, error) {
	roles, err := a.listRoles()
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		name := strings.ToLower(
			strings.TrimSpace(role.Name),
		)
		if name != "" {
			allowed[name] = struct{}{}
		}
	}

	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))

	for _, value := range values {
		role := strings.ToLower(
			strings.TrimSpace(value),
		)

		if role == "" {
			continue
		}

		if _, ok := allowed[role]; !ok {
			return nil, fmt.Errorf(
				"role %q does not exist",
				role,
			)
		}

		if _, ok := seen[role]; ok {
			continue
		}

		seen[role] = struct{}{}
		normalized = append(normalized, role)
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
