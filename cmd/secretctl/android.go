package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const androidAlias = "suburbanremote"

var androidSecretNames = []string{"ANDROID_SIGNING_KEYSTORE_B64", "ANDROID_KEYSTORE_PASSWORD", "ANDROID_KEY_PASSWORD"}

type androidPasswords struct {
	Store string `json:"store"`
	Key   string `json:"key"`
}
type androidRecovery struct {
	Keystore          []byte
	StorePassword     []byte
	KeyPassword       []byte
	CertificateSHA256 string
}
type loadAndroidRecovery func(string, bool) (androidRecovery, error)
type recoveryManifest struct {
	Schema            int    `json:"schema"`
	Repository        string `json:"repository"`
	Alias             string `json:"alias"`
	CertificateSHA256 string `json:"certificateSHA256"`
}

func defaultRecoveryDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".suburban-remote", "recovery")
}

func generateAndroid(run runCommand, dir string, output io.Writer, load loadAndroidRecovery) error {
	secrets, err := inventory(run, "secret")
	if err != nil {
		return err
	}
	variables, err := inventory(run, "variable")
	if err != nil {
		return err
	}
	existing := false
	for _, name := range androidSecretNames {
		if _, ok := secrets[name]; ok {
			existing = true
		}
	}
	_, markerExists := variables["ANDROID_SIGNING_CERT_SHA256"]
	if existing && !markerExists {
		return errors.New("Android signing secrets exist without their recovery certificate marker; no signing secrets were changed")
	}
	if _, ok := variables["ANDROID_KEY_ALIAS"]; ok {
		value, e := run([]string{"variable", "get", "ANDROID_KEY_ALIAS", "--repo", repository}, nil)
		if e != nil {
			return e
		}
		if strings.TrimSpace(string(value)) != androidAlias {
			return errors.New("existing Android alias does not match suburbanremote; no signing secrets were changed")
		}
	}
	material, err := load(dir, !existing && !markerExists)
	if err != nil {
		return err
	}
	defer clear(material.Keystore)
	defer clear(material.StorePassword)
	defer clear(material.KeyPassword)
	if base64.StdEncoding.EncodedLen(len(material.Keystore)) > 48*1024 {
		return errors.New("encoded Android keystore exceeds GitHub secret limit; no Android values were written")
	}
	if markerExists {
		value, e := run([]string{"variable", "get", "ANDROID_SIGNING_CERT_SHA256", "--repo", repository}, nil)
		if e != nil {
			return e
		}
		if strings.TrimSpace(string(value)) != material.CertificateSHA256 {
			return errors.New("Android recovery certificate differs from GitHub marker; no signing secrets were changed")
		}
	}
	// Persist public identity first. A partial write can only resume from this same recovery key.
	for _, item := range []struct{ name, value string }{{"ANDROID_SIGNING_CERT_SHA256", material.CertificateSHA256}, {"ANDROID_KEY_ALIAS", androidAlias}} {
		if _, ok := variables[item.name]; !ok {
			if _, err := run([]string{"variable", "set", item.name, "--repo", repository}, []byte(item.value)); err != nil {
				return fmt.Errorf("%s: write failed", item.name)
			}
		}
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(material.Keystore))
	defer clear(encoded)
	values := [][]byte{encoded, material.StorePassword, material.KeyPassword}
	for i, name := range androidSecretNames {
		if _, exists := secrets[name]; exists {
			fmt.Fprintf(output, "%s: preserved\n", name)
			continue
		}
		if _, err := run([]string{"secret", "set", name, "--repo", repository}, values[i]); err != nil {
			return fmt.Errorf("%s: write failed; retry with the same recovery directory", name)
		}
	}
	verified, err := inventory(run, "secret")
	if err != nil {
		return err
	}
	for _, name := range androidSecretNames {
		if entry, ok := verified[name]; !ok || entry.UpdatedAt == "" {
			return fmt.Errorf("%s: metadata verification failed", name)
		}
	}
	verifiedVars, err := inventory(run, "variable")
	if err != nil {
		return err
	}
	for _, name := range []string{"ANDROID_SIGNING_CERT_SHA256", "ANDROID_KEY_ALIAS"} {
		if entry, ok := verifiedVars[name]; !ok || entry.UpdatedAt == "" {
			return fmt.Errorf("%s: metadata verification failed", name)
		}
	}
	fmt.Fprintf(output, "Android signing secrets: installed or preserved; metadata verified\nRecovery signing proof: passed; certificate SHA-256 %s\n", material.CertificateSHA256)
	return nil
}

func realAndroidRecovery(dir string, allowCreate bool) (androidRecovery, error) {
	var result androidRecovery
	abs, err := recoveryLocation(dir)
	if err != nil {
		return result, err
	}
	if err := secureRecoveryDirectory(abs); err != nil {
		return result, errors.New("cannot establish owner-only recovery directory")
	}
	passwordPath := filepath.Join(abs, "android-passwords.dpapi")
	keystorePath := filepath.Join(abs, "suburbanremote.jks")
	manifestPath := filepath.Join(abs, "android-recovery.json")
	var passwords androidPasswords
	protected, err := os.ReadFile(passwordPath)
	if os.IsNotExist(err) {
		if !allowCreate {
			return result, errors.New("existing Android signing identity requires its original local recovery directory")
		}
		for _, path := range []string{keystorePath, manifestPath} {
			if _, e := os.Stat(path); !os.IsNotExist(e) {
				return result, errors.New("partial recovery files without protected passwords; refusing replacement")
			}
		}
		for _, target := range []*string{&passwords.Store, &passwords.Key} {
			var raw [32]byte
			if _, err := rand.Read(raw[:]); err != nil {
				return result, err
			}
			*target = base64.RawURLEncoding.EncodeToString(raw[:])
			clear(raw[:])
		}
		plain, e := json.Marshal(passwords)
		if e != nil {
			return result, e
		}
		protected, e = protectRecovery(plain)
		clear(plain)
		if e != nil {
			return result, errors.New("cannot protect recovery passwords")
		}
		if e = writeNewFile(passwordPath, protected); e != nil {
			return result, errors.New("cannot save protected recovery passwords")
		}
	} else if err != nil {
		return result, errors.New("cannot read protected recovery passwords")
	}
	plain, err := unprotectRecovery(protected)
	if err != nil {
		return result, errors.New("recovery passwords cannot be opened by this Windows user")
	}
	err = json.Unmarshal(plain, &passwords)
	clear(plain)
	if err != nil || len(passwords.Store) < 32 || len(passwords.Key) < 32 {
		return result, errors.New("invalid protected recovery passwords")
	}
	env := []string{"SUBURBAN_STORE_PASSWORD=" + passwords.Store, "SUBURBAN_KEY_PASSWORD=" + passwords.Key}
	if _, err := os.Stat(keystorePath); os.IsNotExist(err) {
		if !allowCreate {
			return result, errors.New("recovery keystore is missing; refusing to generate a replacement")
		}
		_, err = runSigningTool("keytool", []string{"-genkeypair", "-noprompt", "-storetype", "JKS", "-keystore", keystorePath, "-alias", androidAlias, "-keyalg", "RSA", "-keysize", "3072", "-sigalg", "SHA256withRSA", "-validity", "10000", "-dname", "CN=Suburban Remote, O=Jeremy Proffitt, C=US", "-storepass:env", "SUBURBAN_STORE_PASSWORD", "-keypass:env", "SUBURBAN_KEY_PASSWORD"}, env)
		if err != nil {
			return result, errors.New("Android signing key generation failed; existing recovery files preserved")
		}
	} else if err != nil {
		return result, errors.New("cannot access recovery keystore")
	}
	certificate, err := runSigningTool("keytool", []string{"-exportcert", "-storetype", "JKS", "-keystore", keystorePath, "-alias", androidAlias, "-storepass:env", "SUBURBAN_STORE_PASSWORD"}, env)
	if err != nil {
		return result, errors.New("cannot read public signing certificate from recovery")
	}
	cert, err := x509.ParseCertificate(certificate)
	if err != nil {
		return result, errors.New("invalid signing certificate")
	}
	public, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || public.N.BitLen() < 3072 || cert.NotAfter.Sub(cert.NotBefore) < 25*365*24*time.Hour {
		return result, errors.New("recovery signing certificate fails key strength or validity requirements")
	}
	digest := sha256.Sum256(certificate)
	result.CertificateSHA256 = fmt.Sprintf("%x", digest)
	want := recoveryManifest{Schema: 1, Repository: repository, Alias: androidAlias, CertificateSHA256: result.CertificateSHA256}
	manifestBytes, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		if !allowCreate {
			return result, errors.New("recovery manifest missing for existing signing identity")
		}
		manifestBytes, _ = json.MarshalIndent(want, "", "  ")
		if err = writeNewFile(manifestPath, manifestBytes); err != nil {
			return result, errors.New("cannot save recovery manifest")
		}
	} else if err != nil {
		return result, errors.New("cannot read recovery manifest")
	}
	var got recoveryManifest
	if json.Unmarshal(manifestBytes, &got) != nil || got != want {
		return result, errors.New("recovery signing certificate does not match recorded identity")
	}
	if err := proveRecoverySigning(abs, keystorePath, env); err != nil {
		return result, err
	}
	result.Keystore, err = os.ReadFile(keystorePath)
	if err != nil {
		return result, errors.New("cannot read verified recovery keystore")
	}
	result.StorePassword = []byte(passwords.Store)
	result.KeyPassword = []byte(passwords.Key)
	return result, nil
}

func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func runSigningTool(tool string, args, environment []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, args...)
	hideProcess(cmd)
	cmd.Stdin = bytes.NewReader(nil)
	cmd.Env = append(os.Environ(), environment...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("signing tool failed or timed out")
	}
	return output.Bytes(), nil
}

func proveRecoverySigning(dir, keystore string, env []string) error {
	f, err := os.CreateTemp(dir, "signing-proof-*.jar")
	if err != nil {
		return errors.New("cannot create signing proof")
	}
	path := f.Name()
	defer os.Remove(path)
	w := zip.NewWriter(f)
	entry, err := w.Create("proof.txt")
	if err == nil {
		_, err = entry.Write([]byte("Suburban Remote synthetic signing recovery proof\n"))
	}
	closeErr := w.Close()
	fileErr := f.Close()
	if err != nil || closeErr != nil || fileErr != nil {
		return errors.New("cannot write signing proof")
	}
	_, err = runSigningTool("jarsigner", []string{"-keystore", keystore, "-storetype", "JKS", "-storepass:env", "SUBURBAN_STORE_PASSWORD", "-keypass:env", "SUBURBAN_KEY_PASSWORD", "-sigalg", "SHA256withRSA", "-digestalg", "SHA-256", path, androidAlias}, env)
	if err != nil {
		return errors.New("recovery key could not sign synthetic fixture")
	}
	output, err := runSigningTool("jarsigner", []string{"-verify", "-strict", "-keystore", keystore, "-storetype", "JKS", "-storepass:env", "SUBURBAN_STORE_PASSWORD", path, androidAlias}, env)
	if err != nil || !bytes.Contains(output, []byte("jar verified")) {
		return errors.New("recovery fixture signature verification failed")
	}
	return nil
}
