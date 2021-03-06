// Package identity encapsulates the node identity.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"path/filepath"

	"github.com/oasislabs/oasis-core/go/common/crypto/signature"
	"github.com/oasislabs/oasis-core/go/common/crypto/signature/signers/memory"
	tlsCert "github.com/oasislabs/oasis-core/go/common/crypto/tls"
)

const (
	// NodeKeyPubFilename is the filename of the PEM encoded node public key.
	NodeKeyPubFilename = "identity_pub.pem"

	// P2PKeyPubFilename is the filename of the PEM encoded p2p public key.
	P2PKeyPubFilename = "p2p_pub.pem"

	// ConsensusKeyPubFilename is the filename of the PEM encoded consensus
	// public key.
	ConsensusKeyPubFilename = "consensus_pub.pem"

	// CommonName is the CommonName to use when generating TLS certificates.
	CommonName = "oasis-node"

	tlsKeyFilename  = "tls_identity.pem"
	tlsCertFilename = "tls_identity_cert.pem"
)

// Identity is a node identity.
type Identity struct {
	// NodeSigner is a node identity key signer.
	NodeSigner signature.Signer
	// P2PSigner is a node P2P link key signer.
	P2PSigner signature.Signer
	// ConsensusSigner is a node consensus key signer.
	ConsensusSigner signature.Signer
	// TLSSigner is a node TLS certificate signer.
	TLSSigner signature.Signer
	// TLSCertificate is a certificate that can be used for TLS.
	TLSCertificate *tls.Certificate
}

// Load loads an identity.
func Load(dataDir string, signerFactory signature.SignerFactory) (*Identity, error) {
	return doLoadOrGenerate(dataDir, signerFactory, false)
}

// LoadOrGenerate loads or generates an identity.
func LoadOrGenerate(dataDir string, signerFactory signature.SignerFactory) (*Identity, error) {
	return doLoadOrGenerate(dataDir, signerFactory, true)
}

func doLoadOrGenerate(dataDir string, signerFactory signature.SignerFactory, shouldGenerate bool) (*Identity, error) {
	var signers []signature.Signer
	for _, v := range []struct {
		role  signature.SignerRole
		pubFn string
	}{
		{signature.SignerNode, NodeKeyPubFilename},
		{signature.SignerP2P, P2PKeyPubFilename},
		{signature.SignerConsensus, ConsensusKeyPubFilename},
	} {
		signer, err := signerFactory.Load(v.role)
		switch err {
		case nil:
		case signature.ErrNotExist:
			if !shouldGenerate {
				return nil, err
			}
			if signer, err = signerFactory.Generate(v.role, rand.Reader); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}

		var checkPub signature.PublicKey
		if err = checkPub.LoadPEM(filepath.Join(dataDir, v.pubFn), signer); err != nil {
			return nil, err
		}

		signers = append(signers, signer)
	}

	// TLS certificate.
	//
	// TODO: The key and cert could probably be made totally ephemeral, as long
	// as the registry update takes effect immediately.
	var (
		cert *tls.Certificate
		err  error
	)
	tlsCertPath, tlsKeyPath := TLSCertPaths(dataDir)
	if shouldGenerate {
		cert, err = tlsCert.LoadOrGenerate(tlsCertPath, tlsKeyPath, CommonName)
	} else {
		cert, err = tlsCert.Load(tlsCertPath, tlsKeyPath)
	}
	if err != nil {
		return nil, err
	}

	return &Identity{
		NodeSigner:      signers[0],
		P2PSigner:       signers[1],
		ConsensusSigner: signers[2],
		TLSSigner:       memory.NewFromRuntime(cert.PrivateKey.(ed25519.PrivateKey)),
		TLSCertificate:  cert,
	}, nil
}

// TLSCertPaths returns the TLS private key and certificate paths relative
// to the passed data directory.
func TLSCertPaths(dataDir string) (string, string) {
	var (
		tlsKeyPath  = filepath.Join(dataDir, tlsKeyFilename)
		tlsCertPath = filepath.Join(dataDir, tlsCertFilename)
	)

	return tlsCertPath, tlsKeyPath
}
