package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestDeploymentCanary(t *testing.T) {
	value := "synthetic-test-value"
	digest := sha256.Sum256([]byte(value))
	expected := hex.EncodeToString(digest[:])
	if !canaryValid(value, expected) {
		t.Fatal("matching synthetic input rejected")
	}
	if canaryValid("", expected) || canaryValid(value, "") || canaryValid("wrong", expected) {
		t.Fatal("invalid canary accepted")
	}
}
