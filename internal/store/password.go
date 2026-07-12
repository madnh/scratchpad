package store

import (
	"crypto/rand"
	"math/big"

	"golang.org/x/crypto/bcrypt"
)

// passwordAlphabet avoids visually-confusable characters (l/1, o/0) — a human relays
// the password between agent sessions, sometimes by retyping it.
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyz23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// passwordLength gives ~90+ bits of entropy over the alphabet above.
const passwordLength = 16

// GeneratePassword returns a fresh random pad password. The server always generates
// passwords (never the caller): the value is returned exactly once at create time.
func GeneratePassword() (string, error) {
	out := make([]byte, passwordLength)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}

// HashPassword returns the bcrypt hash stored in a protected pad's header.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// checkPassword verifies access to a pad. Unprotected pads accept anything. For a
// protected pad, a missing and a wrong password yield the SAME error — one uniform
// message, so the response doesn't leak which of the two it was.
func checkPassword(hash, password string) error {
	if hash == "" {
		return nil
	}
	if password == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return coded(CodeUnauthorized, "this pad is password-protected and the password is missing or wrong")
	}
	return nil
}
