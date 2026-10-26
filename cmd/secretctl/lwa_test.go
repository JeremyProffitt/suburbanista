package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLWAPKCEAndReturnBinding(t *testing.T) {
	id := "amzn1.application-oa2-client.synthetic"
	authorize, verifier, state, err := newLWARequest(id)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authorize)
	q := u.Query()
	hash := sha256.Sum256([]byte(verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(hash[:]) || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "profile:user_id" || len(verifier) != 43 || len(state) != 43 {
		t.Fatal("incorrect PKCE request")
	}
	if strings.Contains(authorize, verifier) {
		t.Fatal("verifier exposed in URL")
	}
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	callback := lwaCallback + "?" + url.Values{"state": {state}, "code": {"synthetic-code"}}.Encode()
	if code, err := lwaRedirectCode(callback, state, now, expires); err != nil || code != "synthetic-code" {
		t.Fatal("valid callback rejected")
	}
	for _, bad := range []string{strings.Replace(callback, "suburban.jeremy.ninja", "evil.invalid", 1), callback + "&state=other", callback + "#access_token=unexpected", strings.Replace(callback, state, "wrong", 1)} {
		if _, err := lwaRedirectCode(bad, state, now, expires); err == nil {
			t.Fatal("unbound callback accepted")
		}
	}
	if _, err := lwaRedirectCode(callback, state, expires, expires); err == nil {
		t.Fatal("expired callback accepted")
	}
}

func TestLWAOwnerProof(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		wrongAudience, public bool
	}{{"valid", false, false}, {"wrong-audience", true, false}, {"public-pkce", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			clientID := "amzn1.application-oa2-client.synthetic"
			secret := []byte("synthetic-secret")
			if tc.public {
				secret = nil
			}
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				body := ""
				switch r.URL.Path {
				case "/auth/o2/token":
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if r.Method != "POST" || r.PostForm.Get("code_verifier") != "synthetic-verifier" || r.PostForm.Get("client_secret") != string(secret) || r.PostForm.Get("redirect_uri") != lwaCallback {
						t.Fatal("bad code exchange")
					}
					body = `{"access_token":"synthetic-access","token_type":"bearer","expires_in":3600}`
					if !tc.public {
						body = `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","token_type":"bearer","expires_in":3600}`
					}
				case "/auth/o2/tokeninfo":
					if r.URL.Query().Get("access_token") != "synthetic-access" {
						t.Fatal("missing tokeninfo credential")
					}
					audience := clientID
					if tc.wrongAudience {
						audience = "other-client"
					}
					data, _ := json.Marshal(map[string]string{"aud": audience})
					body = string(data)
				case "/user/profile":
					if r.Header.Get("Authorization") != "Bearer "+"synthetic-access" {
						t.Fatal("profile credential missing")
					}
					body = `{"user_id":"amzn1.account.synthetic-owner"}`
				default:
					t.Fatal("unexpected endpoint")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
			})}
			owner, err := exchangeLWAOwner(client, clientID, secret, "synthetic-code", "synthetic-verifier")
			if tc.wrongAudience {
				if err == nil || calls != 2 {
					t.Fatal("foreign audience reached profile")
				}
			} else if err != nil || owner != "amzn1.account.synthetic-owner" || calls != 3 {
				t.Fatal("owner proof failed")
			}
		})
	}
}

func TestLWAInstallPreservesExisting(t *testing.T) {
	values := map[string]map[string]string{"secret": {}, "variable": {}}
	run := func(args []string, input []byte) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "synthetic-secret") {
			t.Fatal("secret in arguments")
		}
		if args[1] == "list" {
			var rows []metadata
			for name := range values[args[0]] {
				rows = append(rows, metadata{Name: name, UpdatedAt: "2026-10-22T00:00:00Z"})
			}
			return json.Marshal(rows)
		}
		if args[1] == "get" {
			return []byte(values[args[0]][args[2]]), nil
		}
		if _, exists := values[args[0]][args[2]]; exists {
			t.Fatal("existing value overwritten")
		}
		values[args[0]][args[2]] = string(input)
		return nil, nil
	}
	var out bytes.Buffer
	prompts, proofs := 0, 0
	prompt := func(label string) ([]byte, error) {
		prompts++
		switch label {
		case "LWA_CLIENT_ID (empty skips setup): ":
			return []byte("shared-client"), nil
		case "LWA_CLIENT_SECRET (hidden; empty skips setup): ":
			return []byte("synthetic-secret"), nil
		default:
			t.Fatal("unexpected extra profile prompt")
			return nil, nil
		}
	}
	prove := func(id string, secret []byte) (string, error) {
		proofs++
		if id != "shared-client" || string(secret) != "synthetic-secret" {
			t.Fatal("wrong shared profile proof")
		}
		return "amzn1.account.owner", nil
	}
	if err := setupLWA(run, prompt, prove, &out); err != nil {
		t.Fatal(err)
	}
	if prompts != 2 || proofs != 1 || len(values["variable"]) != 2 || values["variable"]["LWA_CLIENT_ID"] != "shared-client" {
		t.Fatal("setup did not use exactly one profile")
	}
	if err := installLWA(run, "shared-client", "amzn1.account.owner", nil, &out); err != nil {
		t.Fatal(err)
	}
	if err := installLWA(run, "different", "amzn1.account.owner", nil, &out); err == nil {
		t.Fatal("changed binding accepted")
	}
	if strings.Contains(out.String(), "synthetic-secret") {
		t.Fatal("secret printed")
	}
}

func TestLWASharedProfileEmptySkips(t *testing.T) {
	for _, skipSecret := range []bool{false, true} {
		writes, proofs := 0, 0
		run := func(args []string, input []byte) ([]byte, error) {
			if args[1] == "list" {
				return []byte("[]"), nil
			}
			writes++
			return nil, nil
		}
		prompt := func(label string) ([]byte, error) {
			if skipSecret && strings.HasPrefix(label, "LWA_CLIENT_ID ") {
				return []byte("shared-client"), nil
			}
			return nil, nil
		}
		var out bytes.Buffer
		if err := setupLWA(run, prompt, func(string, []byte) (string, error) { proofs++; return "", nil }, &out); err != nil {
			t.Fatal(err)
		}
		if writes != 0 || proofs != 0 {
			t.Fatal("empty input did not preserve configuration")
		}
	}
}
