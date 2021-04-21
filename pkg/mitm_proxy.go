package pkg

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"

	"github.com/kr/mitm"
	"github.com/pkg/errors"

	"github.com/wk8/github-api-proxy/pkg/types"
)

type MITMProxy struct {
	requestHandler RequestHandlerI

	server *http.Server
}

func NewMITMProxy(requestHandler RequestHandlerI) *MITMProxy {
	return &MITMProxy{
		requestHandler: requestHandler,
	}
}

// Start starts a HTTP proxy listening on localPort; tlsConfig cannot be nil.
// This is a blocking call.
func (p *MITMProxy) Start(localPort int, tlsConfig *types.TLSConfig) error {
	return p.start(localPort, tlsConfig, nil)
}

// start is the same as Start, except that if it's passed a listeningChan
// then it will close it when it's started listening;
// Useful for tests.
func (p *MITMProxy) start(localPort int, tlsConfig *types.TLSConfig, listeningChan chan interface{}) error {
	if p.server != nil {
		return errors.New("proxy already started")
	}

	ca, err := p.loadCA(tlsConfig)
	if err != nil {
		return errors.Wrap(err, "unable to load TLSInfo")
	}

	p.server = &http.Server{
		Addr: fmt.Sprintf(":%d", localPort),
		Handler: &mitm.Proxy{
			CA: &ca,
			Wrap: func(_upstream http.Handler) http.Handler {
				return http.HandlerFunc(p.requestHandler.HandleRequest)
			},
		},
	}

	return startHTTPServer(p.server, nil, listeningChan, "MITM proxy")
}

func (p *MITMProxy) loadCA(tlsConfig *types.TLSConfig) (cert tls.Certificate, err error) {
	cert, err = tls.LoadX509KeyPair(tlsConfig.CrtPath, tlsConfig.KeyPath)
	if err == nil {
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	}
	return
}
