package auth

import "testing"

func TestSignAndParseTokens(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	if err := Configure(Secrets{Access: "access-secret-0123456789abcdef-test", Refresh: "refresh-secret-0123456789abcdef-test"}); err != nil {
		t.Fatal(err)
	}
	accessTok, err := SignAccessToken(Identity{UserID: "user-lan", Role: "CUSTOMER", WarehouseScope: "NONE", Roles: []string{"CUSTOMER"}})
	if err != nil {
		t.Fatalf("unexpected error signing access token: %v", err)
	}
	claims, err := ParseAccessToken(accessTok)
	if err != nil {
		t.Fatalf("unexpected error parsing access token: %v", err)
	}
	if claims.UserID != "user-lan" || claims.Role != "CUSTOMER" || claims.WarehouseScope != "NONE" {
		t.Fatalf("unexpected access claims: %+v", claims)
	}

	// Không cho phép dùng Access Token làm Refresh Token
	if _, err := ParseRefreshToken(accessTok); err == nil {
		t.Fatalf("expected error when parsing access token as refresh token")
	}

	refreshTok, err := SignRefreshToken("staff-wh-hcm", "WAREHOUSE_STAFF", "WH-HCM-01", "tok-1")
	if err != nil {
		t.Fatalf("unexpected error signing refresh token: %v", err)
	}
	refClaims, err := ParseRefreshToken(refreshTok)
	if err != nil {
		t.Fatalf("unexpected error parsing refresh token: %v", err)
	}
	if refClaims.UserID != "staff-wh-hcm" || refClaims.WarehouseScope != "WH-HCM-01" {
		t.Fatalf("unexpected refresh claims: %+v", refClaims)
	}
}
