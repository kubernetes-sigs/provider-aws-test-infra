/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"
)

type TemporarySSHKey struct {
	Public         []byte
	Private        []byte
	Signer         ssh.Signer
	PrivateKeyPath string
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

func LocalSSHKeyExists(keyPrefix string) bool {
	home := os.Getenv("HOME")
	if _, err := os.Stat(home + "/.ssh/" + keyPrefix); err == nil {
		if _, err := os.Stat(home + "/.ssh/" + keyPrefix + ".pub"); err == nil {
			return true
		}
	}
	return false
}

func LoadExistingSSHKey(keyPrefix string) (*TemporarySSHKey, error) {
	home := os.Getenv("HOME")
	publicBytes, err := os.ReadFile(home + "/.ssh/" + keyPrefix + ".pub")
	if err != nil {
		return nil, fmt.Errorf("error loading public key bytes: %w", err)
	}
	privateBytes, err := os.ReadFile(home + "/.ssh/" + keyPrefix)
	if err != nil {
		return nil, fmt.Errorf("error loading private key bytes: %w", err)
	}
	privateKey, err := ssh.ParseRawPrivateKey(privateBytes)
	if err != nil {
		return nil, fmt.Errorf("error parsing private key bytes: %w", err)
	}
	privateKeySigner, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("error new signer for private key bytes: %w", err)
	}
	return &TemporarySSHKey{
		Public:         publicBytes,
		Private:        privateBytes,
		Signer:         privateKeySigner,
		PrivateKeyPath: home + "/.ssh/" + keyPrefix,
	}, nil
}
