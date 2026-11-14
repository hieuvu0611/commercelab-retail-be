package repository

import (
	"reflect"
	"testing"
)

func TestIdentityBuildsClaimsFromGrants(t *testing.T) {
	tests := []struct {
		name       string
		user       User
		wantRoles  []string
		wantScopes []string
	}{
		{
			name:       "customer has no warehouse scope",
			user:       User{ID: "u1", Role: "CUSTOMER", WarehouseScope: "NONE", Grants: []RoleGrant{{Role: "CUSTOMER"}}},
			wantRoles:  []string{"CUSTOMER"},
			wantScopes: nil,
		},
		{
			name:       "warehouse staff keeps their warehouse",
			user:       User{ID: "u2", Role: "WAREHOUSE_STAFF", WarehouseScope: "WH-HCM-01", Grants: []RoleGrant{{Role: "WAREHOUSE_STAFF", WarehouseID: "WH-HCM-01"}}},
			wantRoles:  []string{"WAREHOUSE_STAFF"},
			wantScopes: []string{"WH-HCM-01"},
		},
		{
			name: "multi-role user: roles and warehouses are merged, de-duplicated and sorted",
			user: User{ID: "u3", Role: "WAREHOUSE_MGR", WarehouseScope: "WH-HN-01", Grants: []RoleGrant{
				{Role: "WAREHOUSE_MGR", WarehouseID: "WH-HN-01"},
				{Role: "WAREHOUSE_STAFF", WarehouseID: "WH-HCM-01"},
				{Role: "WAREHOUSE_STAFF", WarehouseID: "WH-HN-01"},
				{Role: "SUPPORT_AGENT"},
			}},
			wantRoles:  []string{"SUPPORT_AGENT", "WAREHOUSE_MGR", "WAREHOUSE_STAFF"},
			wantScopes: []string{"WH-HCM-01", "WH-HN-01"},
		},
		{
			name:       "warehouse role without a warehouse means every warehouse",
			user:       User{ID: "u4", Role: "WAREHOUSE_MGR", WarehouseScope: "ALL", Grants: []RoleGrant{{Role: "WAREHOUSE_MGR"}}},
			wantRoles:  []string{"WAREHOUSE_MGR"},
			wantScopes: []string{"ALL"},
		},
		{
			name:       "non-warehouse role never gets a scope",
			user:       User{ID: "u5", Role: "FINANCE", WarehouseScope: "ALL", Grants: []RoleGrant{{Role: "FINANCE"}}},
			wantRoles:  []string{"FINANCE"},
			wantScopes: nil,
		},
		{
			name:       "user without grants has no roles",
			user:       User{ID: "u6", Role: "CUSTOMER", WarehouseScope: "NONE"},
			wantRoles:  nil,
			wantScopes: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.user.Identity()
			if id.UserID != tc.user.ID || id.Role != tc.user.Role || id.WarehouseScope != tc.user.WarehouseScope {
				t.Fatalf("legacy fields not carried over: %+v", id)
			}
			if !reflect.DeepEqual(id.Roles, tc.wantRoles) {
				t.Fatalf("roles = %v, want %v", id.Roles, tc.wantRoles)
			}
			if !reflect.DeepEqual(id.WarehouseScopes, tc.wantScopes) {
				t.Fatalf("scopes = %v, want %v", id.WarehouseScopes, tc.wantScopes)
			}
		})
	}
}

func TestHasRole(t *testing.T) {
	u := User{Grants: []RoleGrant{{Role: "SUPPORT_AGENT"}, {Role: "FINANCE"}}}
	if !u.HasRole("FINANCE") || u.HasRole("ACCESS_ADMIN") {
		t.Fatal("HasRole must report granted roles only")
	}
}
