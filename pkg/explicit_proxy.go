package pkg

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/pkg/errors"

	"github.com/wk8/github-api-proxy/pkg/types"
)

type ExplicitProxy struct {
	// the base URL to which we want to redirect the requests
	upstreamBaseURL *url.URL
	requestHandler  RequestHandlerI

	server *http.Server
}

// if upstreamBaseURL is nil, it will be set to defaultUpstreamBaseURL
func NewExplicitProxy(upstreamBaseURL *url.URL, requestHandler RequestHandlerI) *ExplicitProxy {
	return &ExplicitProxy{
		upstreamBaseURL: upstreamBaseURL,
		requestHandler:  requestHandler,
	}
}

// Start starts a HTTP server listening on localPort
// If tlsConfig is nil, will expect plain HTTP.
// This is a blocking call.
func (p *ExplicitProxy) Start(localPort int, tlsConfig *types.TLSConfig) error {
	return p.start(localPort, tlsConfig, nil)
}

// start is the same as Start, except that if it's passed a listeningChan
// then it will close it when it's started listening - useful for tests.
func (p *ExplicitProxy) start(localPort int, tlsConfig *types.TLSConfig, listeningChan chan interface{}) error {
	if p.server != nil {
		return errors.New("proxy already started")
	}

	p.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", localPort),
		Handler: p,
	}

	return startHTTPServer(p.server, tlsConfig, listeningChan, "Explicit proxy")
}

func (p *ExplicitProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	p.requestHandler.HandleGithubAPIRequest(writer, request)
}
