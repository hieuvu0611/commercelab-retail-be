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
)

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

	intlPhonePattern = regexp.MustCompile(`(?:\+[1-9]\d{0,2}[\s.-]?(?:\(\d{1,4}\)|\d{1,4})(?:[\s.-]?\d{2,4}){2,4})`)
	vnPhonePattern   = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(0(?:3|5|7|8|9)\d(?:[\s.-]?\d{3,4}){2})(?:[^A-Za-z0-9_-]|$)`)

	jsonObjectSensitive = regexp.MustCompile(`(?i)"(shippingAddress|shipping_address)"\s*:\s*\{[^{}]*\}`)
	jsonStringSensitive = regexp.MustCompile(`(?i)"(address|line1|street|ward|district|province|recipient|fullName|full_name|phone|phoneNumber|phone_number|password|currentPassword|newPassword|refreshToken|accessToken|authorization|secret)"\s*:\s*"(?:[^"\\]|\\.)*"`)
	kvSensitive         = regexp.MustCompile(`(?i)\b(address|shipping_address|shippingAddress|line1|street|ward|district|province|recipient|fullName|full_name|phone|phoneNumber|phone_number|password|currentPassword|newPassword|refreshToken|accessToken|authorization|secret)=("[^"]*"|'[^']*'|[^\s,&;]+)`)
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
			strings.Contains(lowerKey, "street") || strings.Contains(lowerKey, "ward") ||
			strings.Contains(lowerKey, "district") || strings.Contains(lowerKey, "province"):
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
			strings.Contains(lowerKey, "street") || strings.Contains(lowerKey, "ward") ||
			strings.Contains(lowerKey, "district") || strings.Contains(lowerKey, "province"):
			return key + "=" + RedactedAddress
		default:
			return key + "=" + RedactedValue
		}
	})
	out = emailPattern.ReplaceAllStringFunc(out, Email)
	out = intlPhonePattern.ReplaceAllStringFunc(out, func(match string) string {
		digits := digitsOnly(match)
		if len(digits) < 9 || len(digits) > 15 {
			return match
		}
		return Phone(digits)
	})
	out = vnPhonePattern.ReplaceAllStringFunc(out, func(match string) string {
		sub := vnPhonePattern.FindStringSubmatchIndex(match)
		if len(sub) < 4 {
			return match
		}
		phonePart := match[sub[2]:sub[3]]
		digits := digitsOnly(phonePart)
		if len(digits) < 9 || len(digits) > 11 {
			return match
		}
		return match[:sub[2]] + Phone(digits) + match[sub[3]:]
	})
	return out
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
