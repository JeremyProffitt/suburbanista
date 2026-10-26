package onstar

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
)

type identityClaims struct {
	Subject   string          `json:"sub"`
	Issuer    string          `json:"iss"`
	Audience  json.RawMessage `json:"aud"`
	Expires   int64           `json:"exp"`
	NotBefore int64           `json:"nbf"`
	IssuedAt  int64           `json:"iat"`
}
type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (c *Client) verifyIdentity(ctx context.Context, m metadata, token string) (identityClaims, error) {
	var claims identityClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 128*1024 {
		return claims, failure("invalid-identity-token")
	}
	decode := func(part string, dst any) error {
		data, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, dst)
	}
	var header struct {
		Alg  string   `json:"alg"`
		Kid  string   `json:"kid"`
		Crit []string `json:"crit"`
	}
	if decode(parts[0], &header) != nil || header.Alg != "RS256" || header.Kid == "" || len(header.Crit) != 0 || decode(parts[1], &claims) != nil {
		return claims, failure("invalid-identity-token")
	}
	var keys struct {
		Keys []jwk `json:"keys"`
	}
	if err := c.request(ctx, http.MethodGet, m.JWKSURI, "application/json", "", nil, &keys); err != nil {
		return claims, err
	}
	var key *rsa.PublicKey
	for _, j := range keys.Keys {
		if j.Kid != header.Kid {
			continue
		}
		if key != nil || j.Kty != "RSA" || (j.Use != "" && j.Use != "sig") || (j.Alg != "" && j.Alg != "RS256") {
			return claims, failure("invalid-signing-key")
		}
		n, err := base64.RawURLEncoding.DecodeString(j.N)
		if err != nil {
			return claims, failure("invalid-signing-key")
		}
		e, err := base64.RawURLEncoding.DecodeString(j.E)
		if err != nil || len(e) > 4 {
			return claims, failure("invalid-signing-key")
		}
		exponent := new(big.Int).SetBytes(e).Int64()
		modulus := new(big.Int).SetBytes(n)
		if exponent < 3 || exponent > 2147483647 || exponent%2 == 0 || modulus.BitLen() < 2048 || modulus.BitLen() > 8192 {
			return claims, failure("invalid-signing-key")
		}
		key = &rsa.PublicKey{N: modulus, E: int(exponent)}
	}
	if key == nil {
		return claims, failure("unknown-signing-key")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, failure("invalid-identity-token")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return claims, failure("invalid-identity-signature")
	}
	var audience string
	if json.Unmarshal(claims.Audience, &audience) != nil {
		var audiences []string
		if json.Unmarshal(claims.Audience, &audiences) != nil || len(audiences) != 1 {
			return claims, failure("identity-audience-mismatch")
		}
		audience = audiences[0]
	}
	now := c.now().Unix()
	if claims.Subject == "" || claims.Issuer != m.Issuer || audience != ClientID {
		return claims, failure("identity-claims-mismatch")
	}
	if claims.Expires <= now || claims.NotBefore > now+120 || claims.IssuedAt > now+120 {
		return claims, failure("identity-expired-or-not-yet-valid")
	}
	return claims, nil
}
