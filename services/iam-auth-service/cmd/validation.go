package main

import (
	"net/mail"
	"strings"
	"unicode/utf8"
)

const (
	maxEmailLength    = 254
	minPasswordLength = 10
	// maxPasswordLength bounds the work Argon2id does for one request.
	maxPasswordLength = 128
	maxNameLength     = 100
)

// normalizeEmail trims and lower-cases an email address.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validEmail accepts a bare address like "user@example.com" (no display name, no comments).
func validEmail(email string) bool {
	if email == "" || len(email) > maxEmailLength {
		return false
	}
	parsed, err := mail.ParseAddress(email)
	return err == nil && parsed.Address == email
}

// validatePassword applies the password policy: 10-128 characters and not equal to the email.
func validatePassword(password, email string) []string {
	var problems []string
	length := utf8.RuneCountInString(password)
	if length < minPasswordLength {
		problems = append(problems, "Password must be at least 10 characters")
	}
	if length > maxPasswordLength {
		problems = append(problems, "Password must be at most 128 characters")
	}
	if email != "" && strings.EqualFold(strings.TrimSpace(password), email) {
		problems = append(problems, "Password must not be the same as the email")
	}
	return problems
}

// validateRegistration returns field errors for a registration request (empty when valid).
// email must already be normalized.
func validateRegistration(email, password, fullName string) map[string][]string {
	fieldErrors := map[string][]string{}
	switch {
	case email == "":
		fieldErrors["email"] = []string{"Email is required"}
	case !validEmail(email):
		fieldErrors["email"] = []string{"Email must be a valid address"}
	}
	if problems := validatePassword(password, email); len(problems) != 0 {
		fieldErrors["password"] = problems
	}
	name := strings.TrimSpace(fullName)
	switch {
	case name == "":
		fieldErrors["fullName"] = []string{"Full name is required"}
	case utf8.RuneCountInString(name) > maxNameLength:
		fieldErrors["fullName"] = []string{"Full name must be at most 100 characters"}
	}
	return fieldErrors
}
