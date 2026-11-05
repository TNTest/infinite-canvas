package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
)

func apiKeyAEAD() (cipher.AEAD, error) {
	master, err := hex.DecodeString(config.Cfg.APIKeyEncryptionKey)
	if err != nil || len(master) != 32 {
		return nil, errors.New("API key encryption key is unavailable")
	}
	block, err := aes.NewCipher(master)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func apiKeyAAD(userID string, keyID string) []byte {
	value, _ := json.Marshal([]string{"infinite-canvas-api-key:v1", userID, keyID})
	return value
}

func encryptAPIKey(plaintext string, userID string, keyID string) (string, error) {
	aead, err := apiKeyAEAD()
	if err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, nil, []byte(plaintext), apiKeyAAD(userID, keyID))
	return "v1:" + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func decryptAPIKey(key model.UserAPIKey) (string, error) {
	aead, err := apiKeyAEAD()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(key.EncryptedKey, "v1:") {
		return "", errors.New("API key ciphertext version is invalid")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key.EncryptedKey, "v1:"))
	if err != nil || len(sealed) < aead.Overhead() {
		return "", errors.New("API key ciphertext is invalid")
	}
	plaintext, err := aead.Open(nil, nil, sealed, apiKeyAAD(key.UserID, key.ID))
	if err != nil {
		return "", err
	}
	value := string(plaintext)
	if len(value) != 46 || !strings.HasPrefix(value, "ic_") {
		return "", errors.New("decrypted API key format is invalid")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value[3:])
	if err != nil || len(decoded) != 32 || subtle.ConstantTimeCompare([]byte(apiKeyHash(value)), []byte(key.KeyHash)) != 1 {
		return "", errors.New("decrypted API key digest is invalid")
	}
	return value, nil
}
