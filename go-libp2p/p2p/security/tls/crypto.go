package libp2ptls

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"os"
	"runtime/debug"
	"strings"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
)

const certValidityPeriod = 100 * 365 * 24 * time.Hour // ~100 years
const certificatePrefix = "libp2p-tls-handshake:"
const alpn string = "h3"

var extensionID = getPrefixedExtensionID([]int{1, 1})
var extensionCritical bool // so we can mark the extension critical in tests

type signedKey struct {
	PubKey    []byte
	Signature []byte
}

// Identity is used to secure connections
type Identity struct {
	config tls.Config
}

// IdentityConfig is used to configure an Identity
type IdentityConfig struct {
	CertTemplate *x509.Certificate
	KeyLogWriter io.Writer
}

// IdentityOption transforms an IdentityConfig to apply optional settings.
type IdentityOption func(r *IdentityConfig)

// WithCertTemplate specifies the template to use when generating a new certificate.
func WithCertTemplate(template *x509.Certificate) IdentityOption {
	return func(c *IdentityConfig) {
		c.CertTemplate = template
	}
}

// WithKeyLogWriter optionally specifies a destination for TLS master secrets
// in NSS key log format that can be used to allow external programs
// such as Wireshark to decrypt TLS connections.
// See https://developer.mozilla.org/en-US/docs/Mozilla/Projects/NSS/Key_Log_Format.
// Use of KeyLogWriter compromises security and should only be
// used for debugging.
func WithKeyLogWriter(w io.Writer) IdentityOption {
	return func(c *IdentityConfig) {
		c.KeyLogWriter = w
	}
}

// NewIdentity creates a new identity
func NewIdentity(privKey ic.PrivKey, opts ...IdentityOption) (*Identity, error) {
	config := IdentityConfig{}
	for _, opt := range opts {
		opt(&config)
	}

	var err error
	if config.CertTemplate == nil {
		config.CertTemplate, err = certTemplate()
		if err != nil {
			return nil, err
		}
	}

	cert, err := keyToCertificate(privKey, config.CertTemplate)
	if err != nil {
		return nil, err
	}
	return &Identity{
		config: tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true, // This is not insecure here. We will verify the cert chain ourselves.
			ClientAuth:         tls.RequireAnyClientCert,
			Certificates:       []tls.Certificate{*cert},
			VerifyPeerCertificate: func(_ [][]byte, _ [][]*x509.Certificate) error {
				panic("tls config not specialized for peer")
			},
			NextProtos:             []string{alpn},
			SessionTicketsDisabled: true,
			KeyLogWriter:           config.KeyLogWriter,
			CipherSuites: []uint16{
				tls.TLS_AES_128_GCM_SHA256,                  // Chrome首选
				tls.TLS_AES_256_GCM_SHA384,                  // Chrome次选
				tls.TLS_CHACHA20_POLY1305_SHA256,            // Chrome第三选择
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, // TLS 1.2回退选项
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,   // TLS 1.2回退选项
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, // TLS 1.2回退选项
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,   // TLS 1.2回退选项
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,  // TLS 1.2回退选项
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,    // TLS 1.2回退选项
			},
			CurvePreferences: []tls.CurveID{
				tls.X25519,    // Chrome首选
				tls.CurveP256, // 次选
				tls.CurveP384, // 第三选择
				tls.CurveP521, // 最后选择
			},
		},
	}, nil
}

// ConfigForPeer creates a new single-use tls.Config that verifies the peer's
// certificate chain and returns the peer's public key via the channel. If the
// peer ID is empty, the returned config will accept any peer.
//
// It should be used to create a new tls.Config before securing either an
// incoming or outgoing connection.
func (i *Identity) ConfigForPeer(remote peer.ID) (*tls.Config, <-chan ic.PubKey) {
	keyCh := make(chan ic.PubKey, 1)
	// We need to check the peer ID in the VerifyPeerCertificate callback.
	// The tls.Config it is also used for listening, and we might also have concurrent dials.
	// Clone it so we can check for the specific peer ID we're dialing here.
	conf := i.config.Clone()

	// 内嵌随机CDN域名生成逻辑
	cdnDomains := []string{
		"video{NUM}.cdn.aliyuncs.com",
		"vod{NUM}.gslb.aliyuncs.com",
		"live{NUM}.aliyuncs.com",
		"media{NUM}.alicdn.com",
		"stream{NUM}.alivcdn.com",
		"vod{NUM}.myqcloud.com",
		"live{NUM}.myqcloud.com",
		"video{NUM}.tencent-cdn.com",
		"{NUM}.video.qq.com",
		"{NUM}.livep.gslb.qq.com",
		"cdn{NUM}.myhuaweicloud.com",
		"video{NUM}.hwcdn.com",
		"vod{NUM}.hwclouds-cdn.com",
		"cdn{NUM}.bcebos.com",
		"vd{NUM}.bdstatic.com",
		"cdn{NUM}.ksyun.com",
		"video{NUM}.ksyuncdn.com",
		"vod{NUM}.qiniucdn.com",
		"live{NUM}.qnssl.com",
		"vod{NUM}.ufile.ucloud.cn",
		"{NUM}.cdn{NUM2}.ucloud.cn",
		"vid{NUM}.wsdvs.com",
		"stream{NUM}.wscdns.com",
		"push{NUM}.speedws.com",
		"media{NUM}.chinacache.net",
		"vod{NUM}.ccgslb.com",
		"video{NUM}.qingcdn.com",
		"stream{NUM}.baishancdnx.com",
		"upos-hz-mirror{NUM}.hdslb.com",
		"{NUM}-v{NUM2}-dy.bilivideo.com",
		"{NUM}.mcdn.bilivideo.cn",
		"data{NUM}.video.iqiyi.com",
		"cache{NUM}.video.iqiyi.com",
		"cdn{NUM}.qyvideo.net",
		"valipl{NUM}.cp{NUM2}.alicdn.com",
		"pl{NUM}.youku.com",
		"v{NUM}-dy.volcvideo.com",
		"{NUM}.ctvod.volcvideo.com",
		"sf{NUM}-cdn-tos.douyincdn.com",
		"v{NUM}.stream.kuaishou.com",
		"cdn{NUM}.kuaishou.com",
		"{NUM}.live.ksyun.com",
		"{NUM}.cbn.aliyuncs.com",
		"{NUM}.gslb.dnsv1.com",
	}

	// 使用当前时间作为随机种子
	randSource := rand.NewSource(time.Now().UnixNano())
	rng := rand.New(randSource)

	// 随机选择一个域名模板
	domainTemplate := cdnDomains[rng.Intn(len(cdnDomains))]

	// 替换{NUM}为1-99之间的随机数
	domain := domainTemplate
	for strings.Contains(domain, "{NUM}") {
		randomNum := rng.Intn(99) + 1
		domain = strings.Replace(domain, "{NUM}", fmt.Sprintf("%d", randomNum), 1)
	}

	// 替换{NUM2}为1-99之间的随机数(如果存在)
	for strings.Contains(domain, "{NUM2}") {
		randomNum := rng.Intn(99) + 1
		domain = strings.Replace(domain, "{NUM2}", fmt.Sprintf("%d", randomNum), 1)
	}

	conf.ServerName = domain

	// 添加模拟Chrome浏览器的TLS指纹特征

	// 1. 设置客户端使用的密码套件，顺序模拟Chrome
	conf.CipherSuites = []uint16{
		tls.TLS_AES_128_GCM_SHA256,                  // Chrome首选
		tls.TLS_AES_256_GCM_SHA384,                  // Chrome次选
		tls.TLS_CHACHA20_POLY1305_SHA256,            // Chrome第三选择
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, // TLS 1.2回退选项
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,   // TLS 1.2回退选项
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, // TLS 1.2回退选项
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,   // TLS 1.2回退选项
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,  // TLS 1.2回退选项
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,    // TLS 1.2回退选项
	}

	// 2. 设置曲线偏好，模拟Chrome的设置
	conf.CurvePreferences = []tls.CurveID{
		tls.X25519,    // Chrome首选
		tls.CurveP256, // 次选
		tls.CurveP384, // 第三选择
		tls.CurveP521, // 最后选择
	}

	// We're using InsecureSkipVerify, so the verifiedChains parameter will always be empty.
	// We need to parse the certificates ourselves from the raw certs.
	conf.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) (err error) {
		defer func() {
			if rerr := recover(); rerr != nil {
				fmt.Fprintf(os.Stderr, "panic when processing peer certificate in TLS handshake: %s\n%s\n", rerr, debug.Stack())
				err = fmt.Errorf("panic when processing peer certificate in TLS handshake: %s", rerr)

			}
		}()

		defer close(keyCh)

		chain := make([]*x509.Certificate, len(rawCerts))
		for i := 0; i < len(rawCerts); i++ {
			cert, err := x509.ParseCertificate(rawCerts[i])
			if err != nil {
				return err
			}
			chain[i] = cert
		}

		pubKey, err := PubKeyFromCertChain(chain)
		if err != nil {
			return err
		}
		if remote != "" && !remote.MatchesPublicKey(pubKey) {
			peerID, err := peer.IDFromPublicKey(pubKey)
			if err != nil {
				peerID = peer.ID(fmt.Sprintf("(not determined: %s)", err.Error()))
			}
			return sec.ErrPeerIDMismatch{Expected: remote, Actual: peerID}
		}
		keyCh <- pubKey
		return nil
	}
	return conf, keyCh
}

// PubKeyFromCertChain verifies the certificate chain and extract the remote's public key.
func PubKeyFromCertChain(chain []*x509.Certificate) (ic.PubKey, error) {
	if len(chain) != 1 {
		return nil, errors.New("expected one certificates in the chain")
	}
	cert := chain[0]
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	var found bool
	var keyExt pkix.Extension
	// find the libp2p key extension, skipping all unknown extensions
	for _, ext := range cert.Extensions {
		if extensionIDEqual(ext.Id, extensionID) {
			keyExt = ext
			found = true
			for i, oident := range cert.UnhandledCriticalExtensions {
				if oident.Equal(ext.Id) {
					// delete the extension from UnhandledCriticalExtensions
					cert.UnhandledCriticalExtensions = append(cert.UnhandledCriticalExtensions[:i], cert.UnhandledCriticalExtensions[i+1:]...)
					break
				}
			}
			break
		}
	}
	if !found {
		return nil, errors.New("expected certificate to contain the key extension")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		// If we return an x509 error here, it will be sent on the wire.
		// Wrap the error to avoid that.
		return nil, fmt.Errorf("certificate verification failed: %s", err)
	}

	var sk signedKey
	if _, err := asn1.Unmarshal(keyExt.Value, &sk); err != nil {
		return nil, fmt.Errorf("unmarshalling signed certificate failed: %s", err)
	}
	pubKey, err := ic.UnmarshalPublicKey(sk.PubKey)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling public key failed: %s", err)
	}
	certKeyPub, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return nil, err
	}
	valid, err := pubKey.Verify(append([]byte(certificatePrefix), certKeyPub...), sk.Signature)
	if err != nil {
		return nil, fmt.Errorf("signature verification failed: %s", err)
	}
	if !valid {
		return nil, errors.New("signature invalid")
	}
	return pubKey, nil
}

// GenerateSignedExtension uses the provided private key to sign the public key, and returns the
// signature within a pkix.Extension.
// This extension is included in a certificate to cryptographically tie it to the libp2p private key.
func GenerateSignedExtension(sk ic.PrivKey, pubKey crypto.PublicKey) (pkix.Extension, error) {
	keyBytes, err := ic.MarshalPublicKey(sk.GetPublic())
	if err != nil {
		return pkix.Extension{}, err
	}
	certKeyPub, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		return pkix.Extension{}, err
	}
	signature, err := sk.Sign(append([]byte(certificatePrefix), certKeyPub...))
	if err != nil {
		return pkix.Extension{}, err
	}
	value, err := asn1.Marshal(signedKey{
		PubKey:    keyBytes,
		Signature: signature,
	})
	if err != nil {
		return pkix.Extension{}, err
	}

	return pkix.Extension{Id: extensionID, Critical: extensionCritical, Value: value}, nil
}

// keyToCertificate generates a new ECDSA private key and corresponding x509 certificate.
// The certificate includes an extension that cryptographically ties it to the provided libp2p
// private key to authenticate TLS connections.
func keyToCertificate(sk ic.PrivKey, certTmpl *x509.Certificate) (*tls.Certificate, error) {
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		return nil, err
	}

	// after calling CreateCertificate, these will end up in Certificate.Extensions
	extension, err := GenerateSignedExtension(sk, certKey.Public())
	if err != nil {
		return nil, err
	}
	certTmpl.ExtraExtensions = append(certTmpl.ExtraExtensions, extension)

	certDER, err := x509.CreateCertificate(crand.Reader, certTmpl, certTmpl, certKey.Public(), certKey)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  certKey,
	}, nil
}

// certTemplate returns the template for generating an Identity's TLS certificates.
func certTemplate() (*x509.Certificate, error) {
	bigNum := big.NewInt(1 << 62)
	sn, err := crand.Int(crand.Reader, bigNum)
	if err != nil {
		return nil, err
	}

	subjectSN, err := crand.Int(crand.Reader, bigNum)
	if err != nil {
		return nil, err
	}

	return &x509.Certificate{
		SerialNumber: sn,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certValidityPeriod),
		// According to RFC 3280, the issuer field must be set,
		// see https://datatracker.ietf.org/doc/html/rfc3280#section-4.1.2.4.
		Subject: pkix.Name{SerialNumber: subjectSN.String()},
	}, nil
}
