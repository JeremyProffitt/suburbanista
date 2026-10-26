package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Generated credentials are retained before upload, so retries and owner-run GM
// enrollment use the original identity. GitHub secret values are never read.
type generatedRecovery struct {
	Schema     int               `json:"schema"`
	Repository string            `json:"repository"`
	Keys       map[string]string `json:"keys"`
}

func generateWithRecovery(run runCommand, output io.Writer, dir string) error {
	abs, err := recoveryLocation(dir)
	if err != nil {
		return err
	}
	if err := secureRecoveryDirectory(abs); err != nil {
		return errors.New("cannot establish owner-only recovery directory")
	}
	path := filepath.Join(abs, "generated-keys.dpapi")
	stored := generatedRecovery{Schema: 1, Repository: repository, Keys: map[string]string{}}
	protected, err := os.ReadFile(path)
	if err == nil {
		plain, e := unprotectRecovery(protected)
		if e != nil {
			return errors.New("generated key recovery cannot be opened by this Windows user")
		}
		e = json.Unmarshal(plain, &stored)
		clear(plain)
		if e != nil || stored.Schema != 1 || stored.Repository != repository || stored.Keys == nil {
			return errors.New("invalid generated key recovery")
		}
	} else if !os.IsNotExist(err) {
		return errors.New("cannot read generated key recovery")
	}
	secrets, err := inventory(run, "secret")
	if err != nil {
		return err
	}
	for _, name := range []string{"ONSTAR_DEVICE_ID", "UPDATE_MANIFEST_SIGNING_KEY"} {
		if _, exists := secrets[name]; exists && stored.Keys[name] == "" {
			return errors.New(name + ": existing secret requires its original protected local recovery; refusing replacement")
		}
	}
	variables, err := inventory(run, "variable")
	if err != nil {
		return err
	}
	_, secretExists := secrets["UPDATE_MANIFEST_SIGNING_KEY"]
	_, publicExists := variables["UPDATE_MANIFEST_PUBLIC_KEY"]
	if secretExists || publicExists {
		value, public, e := recoveredKey(stored.Keys["UPDATE_MANIFEST_SIGNING_KEY"], "UPDATE_MANIFEST_SIGNING_KEY")
		if e != nil {
			return e
		}
		defer clear(value)
		if publicExists {
			remote, e := run([]string{"variable", "get", "UPDATE_MANIFEST_PUBLIC_KEY", "--repo", repository}, nil)
			if e != nil {
				return e
			}
			if strings.TrimSpace(string(remote)) != string(public) {
				return errors.New("manifest signing recovery differs from GitHub public key")
			}
		} else {
			if _, e := run([]string{"variable", "set", "UPDATE_MANIFEST_PUBLIC_KEY", "--repo", repository}, public); e != nil {
				return errors.New("manifest public key recovery write failed")
			}
		}
		if !secretExists {
			if _, e := run([]string{"secret", "set", "UPDATE_MANIFEST_SIGNING_KEY", "--repo", repository}, value); e != nil {
				return errors.New("manifest private key recovery write failed")
			}
		}
	}
	return generateUsing(run, output, func(name string) ([]byte, []byte, error) {
		if value := stored.Keys[name]; value != "" {
			return recoveredKey(value, name)
		}
		value, public, e := newKey(name)
		if e != nil {
			return nil, nil, e
		}
		stored.Keys[name] = string(value)
		plain, e := json.Marshal(stored)
		if e != nil {
			clear(value)
			return nil, nil, e
		}
		encrypted, e := protectRecovery(plain)
		clear(plain)
		if e != nil {
			clear(value)
			return nil, nil, errors.New("cannot protect generated key recovery")
		}
		if e = replaceRecoveryFile(path, encrypted); e != nil {
			clear(value)
			return nil, nil, errors.New("cannot persist generated key recovery")
		}
		return value, public, nil
	})
}

func recoveredKey(value, name string) ([]byte, []byte, error) {
	if name == "ONSTAR_DEVICE_ID" {
		if len(value) != 36 {
			return nil, nil, errors.New("invalid recovered device identity")
		}
		return []byte(value), nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, nil, errors.New("invalid recovered key encoding")
	}
	defer clear(raw)
	if name == "UPDATE_MANIFEST_SIGNING_KEY" {
		if len(raw) != ed25519.PrivateKeySize {
			return nil, nil, errors.New("invalid recovered manifest signing key")
		}
		public := ed25519.PrivateKey(raw).Public().(ed25519.PublicKey)
		return []byte(value), []byte(base64.StdEncoding.EncodeToString(public)), nil
	}
	if len(raw) != 32 {
		return nil, nil, errors.New("invalid recovered encryption key")
	}
	return []byte(value), nil, nil
}

func recoveryLocation(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("recovery directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", errors.New("invalid recovery directory")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(cwd, abs)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("recovery directory must be outside the repository")
	}
	return abs, nil
}
func replaceRecoveryFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "protected-*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp, path)
}
