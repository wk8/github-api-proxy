package main

// TODO wkpo reorder
import (
	"io/ioutil"
	"net/url"

	"github.com/wk8/github-api-proxy/pkg/types"

	tokenpools "github.com/wk8/github-api-proxy/pkg/token-pools"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

const defaultUpstreamBaseURL = "https://api.github.com"

type Config struct {
	LogLevel string `yaml:"log_level"`

	// the URL of the upstream Github API - defaults to https://api.github.com
	UpstreamBaseURL string `yaml:"upstream_base_url"`

	TokenSpecs *TokenSpecs `yaml:"token_specs"`

	// the config for the token pool/storage backend to use
	TokenPoolConfig *TokenPoolConfig `yaml:"token_pool"`

	// can be left nil for users who don't want an explicit proxy running
	ExplicitProxyConfig *ProxyConfig `yaml:"explicit_proxy"`

	// can be left nil for users who don't want a MITM proxy running
	MITMProxyConfig *ProxyConfig `yaml:"mitm_proxy"`
}

type TokenSpecs struct {
	// rate limit for token specs that don't define one
	DefaultRateLimit int                `yaml:"default_rate_limit"`
	Specs            []*types.TokenSpec `yaml:"specs"`
}

type TokenPoolConfig struct {
	Backend string `yaml:"backend"`
	// see the chosen backend's `config` struct for details
	Config interface{} `yaml:"config"`
}

type ProxyConfig struct {
	Port int `yaml:"port"`
	// if this is not set, the proxy will listen for plain HTTP (not allowed for MITM proxies)
	TLSConfig *types.TLSConfig `yaml:"tls_config"`
}

func NewConfig(configPath string) (*Config, error) {
	bytes, err := ioutil.ReadFile(configPath)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to read file %q", configPath)
	}

	config := &Config{}
	if err := yaml.Unmarshal(bytes, config); err != nil {
		return nil, errors.Wrapf(err, "%q is not a YAML file", configPath)
	}

	return config, nil
}

func (c *Config) upstreamBaseURL() (*url.URL, error) {
	rawURL := c.UpstreamBaseURL
	if rawURL == "" {
		rawURL = defaultUpstreamBaseURL
	}

	parsedURL, err := url.Parse(rawURL)
	return parsedURL, errors.Wrapf(err, "unable to parse upstream base URL %q", rawURL)
}

func (c *Config) tokenPool() (tokenpools.TokenPoolStorageBackend, error) {
	poolConfig := c.TokenPoolConfig
	if poolConfig == nil {
		return nil, errors.Errorf("pool config required")
	}

	tokenPool, err := tokenpools.NewTokenPoolStorageBackend(poolConfig.Backend, poolConfig.Config)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to build token pool %q", poolConfig.Backend)
	}

	specs, err := c.tokenSpecs()
	if err != nil {
		return nil, errors.Wrapf(err, "invalid token specs config")
	}

	return tokenPool, errors.Wrapf(tokenPool.EnsureTokensAre(specs...), "unable to initialize token pool")
}

func (c *Config) tokenSpecs() ([]*types.TokenSpec, error) {
	if c.TokenSpecs == nil || len(c.TokenSpecs.Specs) == 0 {
		return nil, errors.New("Token specs required")
	}

	for _, spec := range c.TokenSpecs.Specs {
		if spec.ExpectedRateLimit <= 0 {
			if c.TokenSpecs.DefaultRateLimit <= 0 {
				return nil, errors.New("default rate limit required if some token don't define theirs")
			}
			spec.ExpectedRateLimit = c.TokenSpecs.DefaultRateLimit
		}
	}

	return c.TokenSpecs.Specs, nil
}
