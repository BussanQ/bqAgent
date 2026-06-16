package weixin

import (
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// aesKey resolves the 16-byte AES-128 key for an inbound image. It prefers the
// hex-encoded image_item.aeskey, falling back to media.aes_key (base64). The
// second return is false when no key is present (the media is stored plain).
//
// media.aes_key is seen in two encodings in the wild: base64 of the raw 16 bytes,
// or base64 of a 32-char hex string of the key.
func (image InboundImage) aesKey() ([]byte, bool, error) {
	if hexKey := strings.TrimSpace(image.AESKeyHex); hexKey != "" {
		key, err := hex.DecodeString(hexKey)
		if err != nil {
			return nil, false, fmt.Errorf("decode aeskey hex: %w", err)
		}
		if len(key) != 16 {
			return nil, false, fmt.Errorf("aeskey must be 16 bytes, got %d", len(key))
		}
		return key, true, nil
	}

	encoded := strings.TrimSpace(image.AESKeyBase64)
	if encoded == "" {
		return nil, false, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false, fmt.Errorf("decode aes_key base64: %w", err)
	}
	switch {
	case len(decoded) == 16:
		return decoded, true, nil
	case len(decoded) == 32 && isHexString(decoded):
		key, err := hex.DecodeString(string(decoded))
		if err != nil {
			return nil, false, fmt.Errorf("decode aes_key hex: %w", err)
		}
		return key, true, nil
	default:
		return nil, false, fmt.Errorf("aes_key must decode to 16 raw bytes or 32-char hex, got %d bytes", len(decoded))
	}
}

func isHexString(data []byte) bool {
	for _, b := range data {
		switch {
		case b >= '0' && b <= '9', b >= 'a' && b <= 'f', b >= 'A' && b <= 'F':
		default:
			return false
		}
	}
	return true
}

// decryptAESECB decrypts AES-128-ECB ciphertext with PKCS7 padding (matching the
// CDN media encryption) and returns the plaintext.
func decryptAESECB(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	blockSize := block.BlockSize()
	if len(ciphertext) == 0 || len(ciphertext)%blockSize != 0 {
		return nil, fmt.Errorf("ciphertext is not a multiple of the block size")
	}
	plaintext := make([]byte, len(ciphertext))
	for start := 0; start < len(ciphertext); start += blockSize {
		block.Decrypt(plaintext[start:start+blockSize], ciphertext[start:start+blockSize])
	}
	return stripPKCS7(plaintext, blockSize)
}

func stripPKCS7(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty plaintext")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > blockSize || padLen > len(data) {
		return nil, fmt.Errorf("invalid PKCS7 padding")
	}
	for _, b := range data[len(data)-padLen:] {
		if int(b) != padLen {
			return nil, fmt.Errorf("invalid PKCS7 padding")
		}
	}
	return data[:len(data)-padLen], nil
}
