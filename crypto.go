package subspacerelay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha256"
)

type crypto struct {
	aead cipher.AEAD
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
	return c.aead.Seal(nil, nil, b, nil)
}
