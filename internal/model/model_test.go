package model

import (
	"encoding/hex"
	"testing"
)

func TestNewNonce(t *testing.T) {
	a, b := NewNonce(), NewNonce()
	if len(a) != 32 {
		t.Fatalf("len(NewNonce()) = %d, want 32", len(a))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Fatalf("NewNonce() = %q is not hex: %v", a, err)
	}
	if a == b {
		t.Fatalf("two nonces collide: %s", a)
	}
}

func TestEntityIDShort(t *testing.T) {
	id := EntityID("a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0")
	if got, want := id.Short(), "a1b2c3d"; got != want {
		t.Fatalf("Short() = %q, want %q", got, want)
	}
}
