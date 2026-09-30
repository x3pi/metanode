package raftfeed

import "testing"

func TestForwardMACBindsPathAndFields(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	body := []byte("payload")
	base := forwardMAC(secret, "n1", 1000, submitPath, body)
	if base != forwardMAC(secret, "n1", 1000, submitPath, body) {
		t.Fatal("MAC must be deterministic")
	}
	if base == forwardMAC(secret, "n1", 1000, adminStatusPath, body) {
		t.Fatal("MAC must be bound to the endpoint path (cross-endpoint replay)")
	}
	// field-boundary ambiguity: ("n1", ts 1000) vs ("n", ts "11000") must not collide
	if forwardMAC(secret, "n1", 1000, submitPath, body) == forwardMAC(secret, "n", 11000, submitPath, body) {
		t.Fatal("MAC fields must be unambiguously delimited")
	}
}
