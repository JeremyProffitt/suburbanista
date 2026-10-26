package credential

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"
)

func syntheticKeys(active Slot) *Keys {
	k, err := New(bytes.Repeat([]byte{0x31}, 32), bytes.Repeat([]byte{0x72}, 32), active)
	if err != nil {
		panic(err)
	}
	return k
}

var syntheticBinding = Binding{Owner: "synthetic-owner", Provider: "gm", ItemKey: "synthetic-item"}

func TestAuthenticatedMetadata(t *testing.T) {
	k := syntheticKeys(SlotA)
	v, err := k.Encrypt(syntheticBinding, []byte("synthetic-credential"), 1)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		alter func(*Binding, *Value)
	}{
		{"owner", func(b *Binding, v *Value) { b.Owner = "other-owner" }},
		{"provider", func(b *Binding, v *Value) { b.Provider = "lwa" }},
		{"item", func(b *Binding, v *Value) { b.ItemKey = "other-item" }},
		{"slot", func(b *Binding, v *Value) { v.KeySlot = SlotB }},
		{"unknown-slot", func(b *Binding, v *Value) { v.KeySlot = "C" }},
		{"generation", func(b *Binding, v *Value) { v.KeyGeneration++ }},
		{"nonce", func(b *Binding, v *Value) { v.Nonce[0] ^= 1 }},
		{"short-nonce", func(b *Binding, v *Value) { v.Nonce = nil }},
		{"ciphertext", func(b *Binding, v *Value) { v.Ciphertext[0] ^= 1 }},
		{"short-ciphertext", func(b *Binding, v *Value) { v.Ciphertext = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding := syntheticBinding
			value := v
			value.Nonce = bytes.Clone(v.Nonce)
			value.Ciphertext = bytes.Clone(v.Ciphertext)
			tc.alter(&binding, &value)
			got, err := k.Open(context.Background(), binding, value, 4, nil)
			if !errors.Is(err, ErrKeyMismatch) || got != nil {
				t.Fatal("tampered credential did not fail closed")
			}
		})
	}
}

func TestSlotRotation(t *testing.T) {
	a := syntheticKeys(SlotA)
	b := syntheticKeys(SlotB)
	plain := []byte("synthetic-credential")
	v, err := a.Encrypt(syntheticBinding, plain, 4)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	var saved Value
	got, err := b.Open(context.Background(), syntheticBinding, v, 17, func(ctx context.Context, version uint64, replacement Value) error {
		writes++
		if version != 17 {
			t.Fatal("wrong conditional token version")
		}
		saved = replacement
		return nil
	})
	if err != nil || !bytes.Equal(got, plain) || writes != 1 || saved.KeySlot != SlotB || saved.KeyGeneration != 5 {
		t.Fatal("inactive read did not rotate")
	}
	if bytes.Equal(saved.Ciphertext, v.Ciphertext) {
		t.Fatal("ciphertext was not replaced")
	}
	got, err = b.Open(context.Background(), syntheticBinding, saved, 18, func(context.Context, uint64, Value) error { t.Fatal("active slot wrote again"); return nil })
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatal("rotated value cannot be decrypted")
	}
	_, err = a.Open(context.Background(), syntheticBinding, saved, 18, func(_ context.Context, _ uint64, replacement Value) error {
		if replacement.KeyGeneration != 6 || replacement.KeySlot != SlotA {
			t.Fatal("reverse rotation lost generation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRotationFailureNeverReturnsPlaintext(t *testing.T) {
	a := syntheticKeys(SlotA)
	b := syntheticKeys(SlotB)
	for _, generation := range []uint64{1, math.MaxUint64} {
		v, err := a.Encrypt(syntheticBinding, []byte("synthetic-credential"), generation)
		if err != nil {
			t.Fatal(err)
		}
		for _, write := range []ConditionalWrite{nil, func(context.Context, uint64, Value) error {
			return errors.New("synthetic storage failure must not escape")
		}} {
			got, err := b.Open(context.Background(), syntheticBinding, v, 3, write)
			if got != nil || !errors.Is(err, ErrRotationWrite) {
				t.Fatal("failed rotation released plaintext")
			}
		}
	}
}

func TestWrongKeyIsNotProviderRevocation(t *testing.T) {
	a := syntheticKeys(SlotA)
	v, err := a.Encrypt(syntheticBinding, []byte("synthetic-credential"), 1)
	if err != nil {
		t.Fatal(err)
	}
	wrong, _ := New(bytes.Repeat([]byte{9}, 32), bytes.Repeat([]byte{8}, 32), SlotA)
	got, err := wrong.Open(context.Background(), syntheticBinding, v, 1, nil)
	var failure *Failure
	if got != nil || !errors.As(err, &failure) || failure.State != "reauth-required" || failure.Reason != "key-mismatch" {
		t.Fatal("wrong key classification")
	}
}

func TestConfigurationAndAmbiguousAAD(t *testing.T) {
	for _, tc := range []struct {
		a, b   []byte
		active Slot
	}{{make([]byte, 16), make([]byte, 32), SlotA}, {make([]byte, 32), nil, SlotB}, {make([]byte, 32), make([]byte, 32), "C"}} {
		if _, err := New(tc.a, tc.b, tc.active); !errors.Is(err, ErrConfiguration) {
			t.Fatal("bad configuration accepted")
		}
	}
	k := syntheticKeys(SlotA)
	for _, b := range []Binding{{Owner: "", Provider: "gm", ItemKey: "item"}, {Owner: "owner|gm", Provider: "gm", ItemKey: "item"}, {Owner: "owner", Provider: "gm|x", ItemKey: "item"}, {Owner: "owner", Provider: "gm", ItemKey: "item|x"}} {
		if _, err := k.Encrypt(b, []byte("synthetic"), 1); !errors.Is(err, ErrMetadata) {
			t.Fatal("ambiguous AAD accepted")
		}
	}
	if _, err := k.Encrypt(syntheticBinding, []byte("synthetic"), 0); !errors.Is(err, ErrMetadata) {
		t.Fatal("zero generation accepted")
	}
	x, _ := k.Encrypt(syntheticBinding, []byte("synthetic"), 1)
	y, _ := k.Encrypt(syntheticBinding, []byte("synthetic"), 1)
	if bytes.Equal(x.Nonce, y.Nonce) {
		t.Fatal("nonce reused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := k.Open(ctx, syntheticBinding, x, 1, nil); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read accepted")
	}
}
