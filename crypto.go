package subspacerelay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
)

type crypto struct {
	aead cipher.AEAD
}

type messageEncrypter interface {
	encrypt(b []byte) []byte
}

func newCrypto(relayID string) crypto {
	dk, err := pbkdf2.Key(sha256.New, relayID, []byte("aead-crypto-key"), 20, 16)
	if err != nil {
		panic(err)
	}

	block, err := aes.NewCipher(dk)
	if err != nil {
		panic(err)
	}

	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		panic(err)
	}

	return crypto{
		aead: aead,
	}
}

func (c crypto) decrypt(b []byte) ([]byte, error) {
	return c.aead.Open(b[:0], nil, b, nil)
}

func (c crypto) encrypt(b []byte) []byte {
	return c.aead.Seal(b[:0], nil, b, nil)
}

// GenerateECDHAESGCM calculates a secret key between the provided keys and returns an AES128-GCM AEAD mode.
// If privKey is nil a randomly generated key is used
// The public key for our side is always returned.
func GenerateECDHAESGCM(privKey *ecdh.PrivateKey, pubKey *ecdh.PublicKey) (_ cipher.AEAD, _ *ecdh.PublicKey, err error) {
	if privKey == nil {
		privKey, err = ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return
		}
	}

	key, err := privKey.ECDH(pubKey)
	if err != nil {
		return
	}

	block, err := aes.NewCipher(key[:16])
	if err != nil {
		return
	}

	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return
	}

	return aead, privKey.PublicKey(), nil
}
