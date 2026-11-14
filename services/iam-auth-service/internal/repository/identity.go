package repository

import (
	"sort"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
)

// AllWarehouses is the warehouse scope of a warehouse role that is not limited to one warehouse.
const AllWarehouses = "ALL"

// warehouseRoles are the roles whose grants carry a warehouse.
var warehouseRoles = map[string]bool{"WAREHOUSE_STAFF": true, "WAREHOUSE_MGR": true}

// RoleGrant is one row of iam.user_roles. WarehouseID is empty when the grant is not limited to one warehouse.
type RoleGrant struct {
	Role        string
	WarehouseID string
}

// HasRole reports whether the user holds role.
func (u User) HasRole(role string) bool {
	for _, g := range u.Grants {
		if g.Role == role {
			return true
		}
	}
	return false
}

// Identity builds the token identity: de-duplicated, sorted roles, and the warehouses the user may act in.
// A warehouse role granted without a warehouse yields the scope "ALL"; other roles never yield scopes.
func (u User) Identity() auth.Identity {
	roles := map[string]bool{}
	scopes := map[string]bool{}
	for _, g := range u.Grants {
		roles[g.Role] = true
		if !warehouseRoles[g.Role] {
			continue
		}
		if g.WarehouseID == "" {
			scopes[AllWarehouses] = true
		} else {
			scopes[g.WarehouseID] = true
		}
	}
	return auth.Identity{
		UserID:          u.ID,
		Role:            u.Role,
		WarehouseScope:  u.WarehouseScope,
		Roles:           sortedKeys(roles),
		WarehouseScopes: sortedKeys(scopes),
	}
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
