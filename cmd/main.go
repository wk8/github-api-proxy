package main

import (
	"fmt"
	"os"
	"time"

	"github.com/jessevdk/go-flags"
	log "github.com/sirupsen/logrus"

	"github.com/wk8/github-api-proxy/pkg"
	"github.com/wk8/github-api-proxy/version"
)

var opts struct {
	LogLevel   string `long:"log-level" env:"LOG_LEVEL" description:"Log level" default:"info"`
	ConfigPath string `long:"config" env:"CONFIG" description:"Path to config" default:"config.yml"`
	Version    bool   `long:"version" description:"Prints version and exits"`
}

func main() {
	parseArgs()

	if opts.Version {
		fmt.Println("Version:", version.VERSION)
		return
	}

	config, err := NewConfig(opts.ConfigPath)
	if err != nil {
		log.Fatalf("unable to build config %q: %v", opts.ConfigPath, err)
	}

	initLogging(config.LogLevel)

	tokenPool, err := config.tokenPool()
	if err != nil {
		log.Fatalf("Unable to build token pool: %v", err)
	}

	tokensRefreshInterval := time.Minute
	refreshTokens := func() {
		log.Info("Refreshing tokens")
		if err := tokenPool.CheckInTokens(tokensRefreshInterval); err != nil {
			log.Errorf("Error refreshing tokens: %v", err)
		}
	}
	refreshTokens()

	upstreamBaseURL, err := config.upstreamBaseURL()
	if err != nil {
		log.Fatalf("Invalid ustream base URL config: %v", err)
	}

	requestHandler := pkg.NewRequestHandler(upstreamBaseURL, tokenPool)

	running := 0
	errChan := make(chan error)

	if config.ExplicitProxyConfig != nil {
		proxy := pkg.NewExplicitProxy(upstreamBaseURL, requestHandler)
		go func() {
			errChan <- proxy.Start(config.ExplicitProxyConfig.Port, config.ExplicitProxyConfig.TLSConfig)
		}()
		running++
	}

	if config.MITMProxyConfig != nil {
		if config.MITMProxyConfig.TLSConfig == nil {
			log.Fatal("MITM proxies must define a TLS config")
		}

		proxy := pkg.NewMITMProxy(requestHandler)
		go func() {
			errChan <- proxy.Start(config.MITMProxyConfig.Port, config.MITMProxyConfig.TLSConfig)
		}()
		running++
	}

	if running == 0 {
		log.Fatal("No explicit_proxy nor mitm_proxy config, nothing to do!")
	}

	// periodic refresh of tokens
	go func() {
		for range time.Tick(tokensRefreshInterval) {
			refreshTokens()
		}
	}()

	for running != 0 {
		err := <-errChan
		if err != nil {
			log.Fatalf("Proxy error: %v", err)
		}
		running--
	}

	log.Info("Shutting down")
}

func parseArgs() {
	parser := flags.NewParser(&opts, flags.Default)
	if _, err := parser.Parse(); err != nil {
		// If the error was from the parser, then we can simply return
		// as Parse() prints the error already
		if _, ok := err.(*flags.Error); ok {
			os.Exit(1)
		}
		log.Fatalf("Error parsing flags: %v", err)
	}
}

func initLogging(fromConfig string) {
	logLevel := fromConfig
	if fromConfig == "" {
		logLevel = opts.LogLevel
	}

	level, err := log.ParseLevel(logLevel)
	if err != nil {
		log.Fatalf("Unknown log level %s: %v", opts.LogLevel, err)
	}
	log.SetLevel(level)

	// Set the log format to have a reasonable timestamp
	formatter := &log.TextFormatter{
		FullTimestamp: true,
	}
	log.SetFormatter(formatter)

	log.Infof("Log level set to %v", level)
}
