package password

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := Hash("correct horse battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected encoding: %s", hash)
	}

	ok, err := Verify("correct horse battery", hash)
	if err != nil || !ok {
		t.Fatalf("correct password: ok=%v err=%v", ok, err)
	}
	ok, err = Verify("wrong password!!", hash)
	if err != nil || ok {
		t.Fatalf("wrong password must fail without error: ok=%v err=%v", ok, err)
	}
}

func TestHashUsesAFreshSaltEveryTime(t *testing.T) {
	first, _ := Hash("same password 1")
	second, _ := Hash("same password 1")
	if first == second {
		t.Fatal("two hashes of the same password must differ (per-user salt)")
	}
}

func TestVerifyRejectsMalformedAndTamperedHashes(t *testing.T) {
	good, err := Hash("tamper me please")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	parts := strings.Split(good, "$")
	tests := map[string]string{
		"empty":             "",
		"plain sha256":      "8d969eef6ecad3c29a3a629280e686cf0c3f5d5a86aff3ca12020c923adc6c92",
		"wrong algorithm":   strings.Replace(good, "argon2id", "argon2i", 1),
		"bad version":       strings.Replace(good, "v=19", "v=16", 1),
		"missing sections":  "$argon2id$v=19$m=19456,t=2,p=1$onlysalt",
		"bad parameters":    "$argon2id$v=19$m=x,t=2,p=1$" + parts[4] + "$" + parts[5],
		"zero memory":       "$argon2id$v=19$m=0,t=2,p=1$" + parts[4] + "$" + parts[5],
		"bad salt encoding": "$argon2id$v=19$m=19456,t=2,p=1$***$" + parts[5],
		"bad hash encoding": "$argon2id$v=19$m=19456,t=2,p=1$" + parts[4] + "$***",
	}
	for name, hash := range tests {
		t.Run(name, func(t *testing.T) {
			ok, err := Verify("tamper me please", hash)
			if ok {
				t.Fatal("a malformed hash must never verify")
			}
			if err == nil {
				t.Fatal("a malformed hash must report an error")
			}
		})
	}

	// Flipping a character of the digest must fail verification cleanly.
	digest := parts[5]
	flipped := "A"
	if digest[0] == 'A' {
		flipped = "B"
	}
	tampered := strings.Join(append(parts[:5:5], flipped+digest[1:]), "$")
	if ok, err := Verify("tamper me please", tampered); ok || err != nil {
		t.Fatalf("tampered digest: ok=%v err=%v, want false and no error", ok, err)
	}
}

func TestNeedsRehashDetectsOutdatedParameters(t *testing.T) {
	current, _ := Hash("upgrade me please")
	if NeedsRehash(current) {
		t.Fatal("a hash made with current parameters must not need a rehash")
	}
	older := "$argon2id$v=19$m=4096,t=1,p=1$c29tZXNhbHRzb21lc2FsdA$" + strings.Split(current, "$")[5]
	if !NeedsRehash(older) {
		t.Fatal("a hash with weaker parameters must need a rehash")
	}
	if !NeedsRehash("not-a-hash") {
		t.Fatal("a malformed hash must need a rehash")
	}
}
