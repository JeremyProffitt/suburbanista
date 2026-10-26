package verify

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"
)

func TestCanaryDigestEncodings(t *testing.T) {
	secret := "synthetic canary +/with \"quotes\""
	j, _ := json.Marshal(secret)
	forms := []string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), string(j[1 : len(j)-1]), url.QueryEscape(secret)}
	digests := CanaryDigests(forms)
	if err := CheckCanaryDigests([]byte("clean evidence"), digests); err != nil {
		t.Fatal(err)
	}
	for _, form := range forms {
		if CheckCanaryDigests([]byte("prefix "+form+" suffix"), digests) == nil {
			t.Fatal("encoded canary accepted")
		}
	}
	if CheckCanaryDigests(nil, nil) == nil {
		t.Fatal("missing digests accepted")
	}
}
