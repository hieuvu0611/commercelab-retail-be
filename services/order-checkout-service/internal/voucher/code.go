package voucher

import "regexp"

var codePattern = regexp.MustCompile(`^[A-Z0-9_-]{3,32}$`)

// ValidCode reports whether code (already normalised) has the shape of a voucher code.
func ValidCode(code string) bool { return codePattern.MatchString(code) }
