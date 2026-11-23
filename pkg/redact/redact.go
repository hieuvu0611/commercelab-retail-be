package redact

import (
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
)

const (
	RedactedValue   = "[REDACTED]"
	RedactedAddress = "[REDACTED_ADDRESS]"

	sensitiveFieldNames = `address|shipping_address|shippingAddress|billing_address|billingAddress|delivery_address|deliveryAddress|` +
		`line1|line2|street|ward|district|province|recipient|recipientName|recipient_name|fullName|full_name|` +
		`phone|phoneNumber|phone_number|` +
		`password|currentPassword|current_password|newPassword|new_password|` +
		`token|accessToken|access_token|refreshToken|refresh_token|idToken|id_token|authorization|secret`
)

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

	vnIntlPhonePattern   = regexp.MustCompile(`(?:\(\+84\)|\+84)[\s.-]?0?[35789](?:\d{8}|\d{2}[\s.-]\d{3}[\s.-]\d{3}|\d[\s.-]\d{3,4}[\s.-]\d{3,4}|\d{1,2}[\s.-]\d{6,7})`)
	intlPhonePattern     = regexp.MustCompile(`(?:\(\+[1-9]\d{0,2}\)|\+[1-9]\d{0,2})[\s.-]?(?:\(\d{1,4}\)|\d{1,4})(?:[\s.-]?\d{2,4}){2,4}`)
	vnLocalPhonePattern  = regexp.MustCompile(`0[35789](?:\d{8,9}|\d{2}[\s.-]\d{3}[\s.-]\d{3}|\d[\s.-]\d{3,4}[\s.-]\d{3,4}|\d{1,2}[\s.-]\d{6,7})`)
	vnBare84PhonePattern = regexp.MustCompile(`84[\s.-]?[35789](?:\d{8}|\d{2}[\s.-]\d{3}[\s.-]\d{3}|\d[\s.-]\d{3,4}[\s.-]\d{3,4}|\d{1,2}[\s.-]\d{6,7})`)
	bearerTokenPattern   = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)

	jsonObjectSensitive = regexp.MustCompile(`(?i)"(shippingAddress|shipping_address|billingAddress|billing_address|deliveryAddress|delivery_address|address)"\s*:\s*\{[^{}]*\}`)
	jsonStringSensitive = regexp.MustCompile(`(?i)"(` + sensitiveFieldNames + `)"\s*:\s*"(?:[^"\\]|\\.)*"`)
	kvSensitive         = regexp.MustCompile(`(?i)\b(` + sensitiveFieldNames + `)=("[^"]*"|'[^']*'|Bearer\s+[^\s,&;]+|[^\s,&;]+)`)
)

func Email(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ""
	}
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}

func Phone(phone string) string {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return ""
	}
	if len(phone) <= 2 {
		return "***"
	}
	return "***" + phone[len(phone)-2:]
}

func Address(address string) string {
	if strings.TrimSpace(address) == "" {
		return ""
	}
	return RedactedAddress
}

func Text(s string) string {
	if s == "" {
		return ""
	}
	out := jsonObjectSensitive.ReplaceAllString(s, `"$1":"`+RedactedAddress+`"`)
	out = jsonStringSensitive.ReplaceAllStringFunc(out, func(match string) string {
		colon := strings.Index(match, ":")
		keyPart := match[:colon]
		lowerKey := strings.ToLower(keyPart)
		valPart := strings.TrimSpace(match[colon+1:])
		rawVal := strings.Trim(valPart, `"`)
		switch {
		case strings.Contains(lowerKey, "phone"):
			return keyPart + `:"` + Phone(rawVal) + `"`
		case strings.Contains(lowerKey, "address") || strings.Contains(lowerKey, "line1") ||
			strings.Contains(lowerKey, "line2") || strings.Contains(lowerKey, "street") ||
			strings.Contains(lowerKey, "ward") || strings.Contains(lowerKey, "district") ||
			strings.Contains(lowerKey, "province"):
			return keyPart + `:"` + RedactedAddress + `"`
		default:
			return keyPart + `:"` + RedactedValue + `"`
		}
	})
	out = kvSensitive.ReplaceAllStringFunc(out, func(match string) string {
		eq := strings.Index(match, "=")
		key := match[:eq]
		lowerKey := strings.ToLower(key)
		rawVal := strings.Trim(match[eq+1:], `"'`)
		switch {
		case strings.Contains(lowerKey, "phone"):
			return key + "=" + Phone(rawVal)
		case strings.Contains(lowerKey, "address") || strings.Contains(lowerKey, "line1") ||
			strings.Contains(lowerKey, "line2") || strings.Contains(lowerKey, "street") ||
			strings.Contains(lowerKey, "ward") || strings.Contains(lowerKey, "district") ||
			strings.Contains(lowerKey, "province"):
			return key + "=" + RedactedAddress
		default:
			return key + "=" + RedactedValue
		}
	})
	out = redactGoStructFields(out)
	out = bearerTokenPattern.ReplaceAllString(out, "Bearer "+RedactedValue)
	out = emailPattern.ReplaceAllStringFunc(out, Email)
	out = replacePhoneMatches(out, vnIntlPhonePattern, 9, 15)
	out = replacePhoneMatches(out, intlPhonePattern, 9, 15)
	out = replacePhoneMatches(out, vnLocalPhonePattern, 9, 11)
	out = replacePhoneMatches(out, vnBare84PhonePattern, 11, 11)
	return out
}

func isPhoneBoundaryByte(b byte) bool {
	return !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-')
}

func replacePhoneMatches(s string, re *regexp.Regexp, minDigits, maxDigits int) string {
	locs := re.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		if start > 0 && !isPhoneBoundaryByte(s[start-1]) {
			continue
		}
		if end < len(s) && !isPhoneBoundaryByte(s[end]) {
			continue
		}
		digits := digitsOnly(s[start:end])
		if len(digits) < minDigits || len(digits) > maxDigits {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(Phone(digits))
		last = end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

type goStructField struct {
	name     string
	start    int
	colonIdx int
}

func isIdentStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func isIdentChar(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

func parseGoFieldPrefix(s string, idx int) (string, int, bool) {
	if idx >= len(s) || !isIdentStart(s[idx]) {
		return "", 0, false
	}
	j := idx + 1
	for j < len(s) && isIdentChar(s[j]) {
		j++
	}
	if j >= len(s) || s[j] != ':' {
		return "", 0, false
	}
	if j+2 < len(s) && s[j+1] == '/' && s[j+2] == '/' {
		return "", 0, false
	}
	return s[idx:j], j, true
}

func isGoFieldStart(body string, idx int) (string, int, bool) {
	name, colonIdx, ok := parseGoFieldPrefix(body, idx)
	if !ok {
		return "", 0, false
	}
	next := colonIdx + 1
	if next == len(body) || body[next] != ' ' {
		return name, colonIdx, true
	}
	if next+1 < len(body) {
		if _, _, nextField := parseGoFieldPrefix(body, next+1); nextField {
			return name, colonIdx, true
		}
	}
	return "", 0, false
}

func findMatchingBrace(s string, openIdx int) int {
	braceDepth := 0
	bracketDepth := 0
	for i := openIdx; i < len(s); i++ {
		switch s[i] {
		case '{':
			braceDepth++
		case '}':
			braceDepth--
			if braceDepth == 0 && bracketDepth == 0 {
				return i
			}
		case '[':
			bracketDepth++
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
		}
	}
	return -1
}

func redactGoStructFields(s string) string {
	if !strings.Contains(s, "{") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	changed := false
	for i < len(s) {
		if s[i] == '{' {
			closeIdx := findMatchingBrace(s, i)
			if closeIdx > i+1 {
				body := s[i+1 : closeIdx]
				if _, _, ok := isGoFieldStart(body, 0); ok {
					redactedBody := redactGoStructBody(body)
					if redactedBody != body {
						changed = true
					}
					b.WriteByte('{')
					b.WriteString(redactedBody)
					b.WriteByte('}')
					i = closeIdx + 1
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	if !changed {
		return s
	}
	return b.String()
}

func redactGoStructBody(body string) string {
	var fields []goStructField
	braceDepth := 0
	bracketDepth := 0
	for i := 0; i < len(body); i++ {
		if braceDepth == 0 && bracketDepth == 0 && (i == 0 || body[i-1] == ' ') {
			if name, colonIdx, ok := isGoFieldStart(body, i); ok {
				fields = append(fields, goStructField{name: name, start: i, colonIdx: colonIdx})
				i = colonIdx
				continue
			}
		}
		switch body[i] {
		case '{':
			braceDepth++
		case '}':
			if braceDepth > 0 {
				braceDepth--
			}
		case '[':
			bracketDepth++
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
		}
	}
	if len(fields) == 0 {
		return body
	}

	var b strings.Builder
	b.Grow(len(body))
	for idx, f := range fields {
		if idx > 0 {
			b.WriteByte(' ')
		}
		valStart := f.colonIdx + 1
		valEnd := len(body)
		if idx+1 < len(fields) {
			valEnd = fields[idx+1].start - 1
		}
		rawVal := ""
		if valEnd > valStart {
			rawVal = body[valStart:valEnd]
		}
		b.WriteString(f.name)
		b.WriteByte(':')
		if masked, sensitive := redactSensitiveGoField(f.name, rawVal); sensitive {
			b.WriteString(masked)
		} else {
			b.WriteString(redactGoStructFields(rawVal))
		}
	}
	return b.String()
}

func redactSensitiveGoField(name, rawVal string) (string, bool) {
	lower := strings.ToLower(name)
	switch lower {
	case "phone", "phonenumber", "phone_number":
		return Phone(rawVal), true
	case "email", "emailaddress", "email_address":
		return Email(rawVal), true
	case "address", "shippingaddress", "shipping_address",
		"billingaddress", "billing_address", "deliveryaddress", "delivery_address",
		"line1", "line2", "street", "ward", "district", "province":
		return Address(rawVal), true
	case "recipient", "recipientname", "recipient_name", "fullname", "full_name",
		"password", "currentpassword", "current_password", "newpassword", "new_password",
		"token", "accesstoken", "access_token", "refreshtoken", "refresh_token",
		"idtoken", "id_token", "authorization", "secret":
		if strings.TrimSpace(rawVal) == "" {
			return "", true
		}
		return RedactedValue, true
	default:
		return rawVal, false
	}
}

func digitsOnly(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

type Writer struct {
	dst io.Writer
}

func WrapWriter(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	if _, ok := w.(*Writer); ok {
		return w
	}
	return &Writer{dst: w}
}

func (w *Writer) Write(p []byte) (int, error) {
	cleaned := Text(string(p))
	if _, err := io.WriteString(w.dst, cleaned); err != nil {
		return 0, err
	}
	return len(p), nil
}

func InstallDefaultLogger() {
	log.SetOutput(WrapWriter(log.Writer()))
}

func Any(v any) string {
	if v == nil {
		return ""
	}
	return Text(fmt.Sprint(v))
}
