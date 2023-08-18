package utils

import (
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

type TemporarySSHKey struct {
	Public  []byte
	Private []byte
	Signer  ssh.Signer
}

func GenerateSSHKeypair() (*TemporarySSHKey, error) {
	privateKey, err := rsa.GenerateKey(crand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating Private key, %w", err)
	}
	if err := privateKey.Validate(); err != nil {
		return nil, fmt.Errorf("validating Private key, %w", err)
	}

	pubSSH, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("creating SSH key, %w", err)
	}
	pubKey := ssh.MarshalAuthorizedKey(pubSSH)

	privDER := x509.MarshalPKCS1PrivateKey(privateKey)
	privBlock := pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   privDER,
	}
	privatePEM := pem.EncodeToMemory(&privBlock)

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("creating Signer, %w", err)
	}
	return &TemporarySSHKey{
		Public:  pubKey,
		Private: privatePEM,
		Signer:  signer,
	}, nil
}
