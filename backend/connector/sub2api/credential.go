package sub2api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/fausto2022/relaydeck/backend/connector"
)

const browserCredentialAlgorithm = "RSA-OAEP-256+A256GCM"

type browserCredentialEnvelope struct {
	Algorithm    string `json:"algorithm"`
	KeyID        string `json:"key_id"`
	EncryptedKey string `json:"encrypted_key"`
	IV           string `json:"iv"`
	Ciphertext   string `json:"ciphertext"`
}

type browserCredentialKey struct {
	Algorithm     string `json:"algorithm"`
	KeyID         string `json:"key_id"`
	PublicKey     string `json:"public_key"`
	ExpiresAt     int64  `json:"expires_at"`
	FlowExpiresAt int64  `json:"flow_expires_at"`
	ServerTime    int64  `json:"server_time"`
}

func (c *Client) buildCredentialEnvelope(
	ctx context.Context,
	ch *connector.Channel,
) (*browserCredentialEnvelope, bool, error) {
	site := strings.TrimRight(ch.SiteURL, "/")
	body, err := c.getJSON(ctx, site+"/api/v1/auth/credential-key", nil)
	if err != nil {
		status := connector.HTTPStatusCode(err)
		if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
			return nil, false, nil
		}
		return nil, false, err
	}
	var key browserCredentialKey
	if err := json.Unmarshal(body, &key); err != nil {
		return nil, true, fmt.Errorf("decode credential key: %w", err)
	}
	if key.Algorithm != browserCredentialAlgorithm {
		return nil, true, fmt.Errorf("unsupported credential algorithm: %s", key.Algorithm)
	}
	if key.KeyID == "" || key.PublicKey == "" {
		return nil, true, errors.New("credential key response is incomplete")
	}
	if key.ServerTime <= 0 || key.ExpiresAt <= key.ServerTime || key.FlowExpiresAt <= key.ServerTime {
		return nil, true, errors.New("credential key is expired")
	}

	publicKey, err := parseCredentialPublicKey(key.PublicKey)
	if err != nil {
		return nil, true, err
	}
	aesKey := make([]byte, 32)
	if _, err := rand.Read(aesKey); err != nil {
		return nil, true, fmt.Errorf("generate AES key: %w", err)
	}
	encryptedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, aesKey, nil)
	if err != nil {
		return nil, true, fmt.Errorf("encrypt AES key: %w", err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, true, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, true, fmt.Errorf("create AES-GCM: %w", err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return nil, true, fmt.Errorf("generate AES-GCM IV: %w", err)
	}
	credentials, err := json.Marshal(map[string]any{
		"email":     ch.Username,
		"password":  ch.Password,
		"issued_at": key.ServerTime,
	})
	if err != nil {
		return nil, true, fmt.Errorf("encode credentials: %w", err)
	}
	ciphertext := gcm.Seal(nil, iv, credentials, []byte(key.KeyID))
	encode := base64.RawURLEncoding.EncodeToString
	return &browserCredentialEnvelope{
		Algorithm:    browserCredentialAlgorithm,
		KeyID:        key.KeyID,
		EncryptedKey: encode(encryptedKey),
		IV:           encode(iv),
		Ciphertext:   encode(ciphertext),
	}, true, nil
}

func parseCredentialPublicKey(encoded string) (*rsa.PublicKey, error) {
	encoded = strings.TrimSpace(encoded)
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		der, err = base64.RawURLEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("decode credential public key: %w", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse credential public key: %w", err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("credential public key is not RSA")
	}
	return publicKey, nil
}
