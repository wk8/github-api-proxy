package main

import (
	"fmt"
	"os"

	"github.com/wk8/github-api-proxy/pkg"

	"github.com/wk8/github-api-proxy/version"

	"github.com/jessevdk/go-flags"
	log "github.com/sirupsen/logrus"
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
			log.Fatalf("MITM proxies must define a TLS config")
		}

		proxy := pkg.NewMITMProxy(requestHandler)
		go func() {
			errChan <- proxy.Start(config.MITMProxyConfig.Port, config.MITMProxyConfig.TLSConfig)
		}()
		running++
	}

	if running == 0 {
		log.Fatalf("No explicit_proxy nor mitm_proxy config, nothing to do!")
	}

	for running != 0 {
		err := <-errChan
		if err != nil {
			log.Fatalf("Proxy error: %v", err)
		}
		running--
	}

	log.Infof("Shutting down")
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
