package service

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
)

func TestAPIKeyEncryption(t *testing.T) {
	previous := config.Cfg
	t.Cleanup(func() { config.Cfg = previous })
	master := strings.Repeat("a", 64)
	config.Cfg.APIKeyEncryptionKey = master
	plaintext := "ic_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	key := model.UserAPIKey{ID: "key-owner-row", UserID: "owner", KeyHash: apiKeyHash(plaintext)}
	first, err := encryptAPIKey(plaintext, key.UserID, key.ID)
	if err != nil {
		t.Fatal("key encryption failed")
	}
	second, err := encryptAPIKey(plaintext, key.UserID, key.ID)
	if err != nil || first == second || !strings.HasPrefix(first, "v1:") || strings.Contains(first, plaintext) {
		t.Fatal("encryption must use a fresh nonce and a versioned ciphertext")
	}
	key.EncryptedKey = first
	if value, err := decryptAPIKey(key); err != nil || value != plaintext {
		t.Fatal("encrypted key did not round-trip")
	}
	// Reconstructing configuration and AEAD with the same master simulates a
	// restart; the login signing secret must have no influence on decryption.
	config.Cfg = config.Config{JWTSecret: "new-session-signing-secret", APIKeyEncryptionKey: master}
	if value, err := decryptAPIKey(key); err != nil || value != plaintext {
		t.Fatal("the same persisted master must decrypt after restart or a new login")
	}
	for _, changed := range []model.UserAPIKey{
		{ID: "other-row", UserID: key.UserID, KeyHash: key.KeyHash, EncryptedKey: first},
		{ID: key.ID, UserID: "other-owner", KeyHash: key.KeyHash, EncryptedKey: first},
		{ID: key.ID, UserID: key.UserID, KeyHash: strings.Repeat("0", 64), EncryptedKey: first},
	} {
		if value, err := decryptAPIKey(changed); err == nil || value != "" {
			t.Fatal("owner, row, and digest bindings must reject transplanted ciphertext")
		}
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(first, "v1:"))
	if err != nil {
		t.Fatal("invalid encrypted fixture")
	}
	sealed[len(sealed)-1] ^= 1
	for _, ciphertext := range []string{
		"v1:" + base64.RawURLEncoding.EncodeToString(sealed),
		"v2:" + strings.TrimPrefix(first, "v1:"),
		"v1:!invalid",
		"v1:AQID",
	} {
		changed := key
		changed.EncryptedKey = ciphertext
		if value, err := decryptAPIKey(changed); err == nil || value != "" {
			t.Fatal("tampered, unsupported, or truncated ciphertext was accepted")
		}
	}
	badPlaintext := "not-an-api-key"
	badCiphertext, err := encryptAPIKey(badPlaintext, key.UserID, key.ID)
	if err != nil {
		t.Fatal("invalid-format fixture encryption failed")
	}
	changed := key
	changed.EncryptedKey, changed.KeyHash = badCiphertext, apiKeyHash(badPlaintext)
	if value, err := decryptAPIKey(changed); err == nil || value != "" {
		t.Fatal("decryption must validate the recovered API key format")
	}
	config.Cfg.APIKeyEncryptionKey = strings.Repeat("b", 64)
	if value, err := decryptAPIKey(key); err == nil || value != "" {
		t.Fatal("a different master key must not decrypt stored keys")
	}
	for _, invalidMaster := range []string{"", strings.Repeat("a", 32), strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("g", 64)} {
		config.Cfg.APIKeyEncryptionKey = invalidMaster
		if value, err := encryptAPIKey(plaintext, key.UserID, key.ID); err == nil || value != "" {
			t.Fatal("missing or malformed master must reject encryption")
		}
		if value, err := decryptAPIKey(key); err == nil || value != "" {
			t.Fatal("missing or malformed master must reject decryption")
		}
	}
}
