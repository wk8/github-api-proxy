package pkg

import (
	"errors"
	"net"
	"net/http"

	log "github.com/sirupsen/logrus"

	"github.com/wk8/github-api-proxy/pkg/types"
)

func startHTTPServer(server *http.Server, tlsInfo *types.TLSConfig, listeningChan chan interface{}, logLineServerName string) error {
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	if listeningChan != nil {
		close(listeningChan)
	}

	if logLineServerName != "" {
		log.Infof("%s server listening on %s", logLineServerName, server.Addr)
	}

	if tlsInfo == nil {
		err = server.Serve(listener)
	} else {
		err = server.ServeTLS(listener, tlsInfo.CrtPath, tlsInfo.KeyPath)
	}

	if errors.Is(err, http.ErrServerClosed) {
		if logLineServerName != "" {
			log.Infof("%s server closed", logLineServerName)
		}
		return nil
	}
	if logLineServerName != "" {
		log.Errorf("%s server closed with error: %v", logLineServerName, err)
	}
	return err
}
