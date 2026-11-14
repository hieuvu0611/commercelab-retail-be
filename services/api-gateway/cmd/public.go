package main

import (
	"net/http"
	"strings"
)

// publicEndpoint is one entry of the allowlist of calls that need no access token.
type publicEndpoint struct {
	method string
	path   string
	// prefix also allows every path below path (path + "/...").
	prefix bool
}

// publicAllowlist is the complete list of unauthenticated calls: reading the public catalog, the sign-in flow
// and health checks. Everything else requires a valid access token.
//
// POST /api/v1/auth/logout is public because the refresh token in its body is the proof, and a client whose
// access token already expired must still be able to sign out.
var publicAllowlist = []publicEndpoint{
	{method: http.MethodGet, path: "/health"},
	{method: http.MethodGet, path: "/api/v1/products", prefix: true},
	{method: http.MethodGet, path: "/api/v1/categories", prefix: true},
	{method: http.MethodPost, path: "/api/v1/auth/login"},
	{method: http.MethodPost, path: "/api/v1/auth/register"},
	{method: http.MethodPost, path: "/api/v1/auth/refresh"},
	{method: http.MethodPost, path: "/api/v1/auth/logout"},
	{method: http.MethodPost, path: "/auth/refresh"}, // deprecated alias of /api/v1/auth/refresh
}

// isPublic reports whether the call is on the allowlist. Matching is exact on method and path, or on a path
// prefix at a segment boundary (so /api/v1/productsx is not the products collection).
func isPublic(method, path string) bool {
	for _, e := range publicAllowlist {
		if e.method != method && !(method == http.MethodHead && e.method == http.MethodGet) {
			continue
		}
		if path == e.path || (e.prefix && strings.HasPrefix(path, e.path+"/")) {
			return true
		}
	}
	return false
}
