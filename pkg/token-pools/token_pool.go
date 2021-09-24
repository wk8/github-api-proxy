package token_pools

import (
	"time"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"

	"github.com/wk8/github-api-proxy/pkg/types"
)

// how often tokens are reset
const ResetInterval = time.Hour

// a TokenPoolStorageBackend abstracts away storing tokens and their metadata
// Implementations can be local, or distributed to allow for several instances to use the same storage.
// All implementations must be thread-safe.
type TokenPoolStorageBackend interface {
	// EnsureTokensAre will be called at init time, to ensure that the storage is in sync with the tokens listed in
	// the configuration.
	// For each given token, this should ensure that the token is present in the storage, and if not,
	// add it and assume it has its full hourly quota available and untouched.
	// Conversely, any token present in storage but not in the config should be deleted.
	EnsureTokensAre(tokens ...*types.TokenSpec) error

	// CheckOutToken checks out a token with its full hourly quota available, if there are any.
	// If there are no token available that hasn't been checked out yet, it should check out the token
	// that has the most calls remaining, if any.
	// The storage should keep track of when the token was checked out, and mark it as checked out
	// (if it was already checked out, the checked-out timestamp should be kept to its original value).
	// Errors are only for real storage errors; if the storage is working as intended, but simply
	// has no remaining token, it should just reply (nil, nil).
	CheckOutToken() (*types.Token, error)

	// UpdateTokenUsage should update a given checked-out token's count of remaining calls.
	// Trying to update a token that's unknown to the storage shouldn't yield an error, as this
	// can happen if a token has previously been checked out, before being removed from the config.
	UpdateTokenUsage(token string, remaining int) error

	// UpdateTokenRateLimit should update a given token's rate limit, in case the config gave
	// a wrong value for it.
	// Same as UpdateTokenUsage, trying to update a token that's unknown to the storage shouldn't yield an error.
	UpdateTokenRateLimit(token string, rateLimit int) error

	// CheckInTokens should make the storage iterate over all checked out tokens, and if they've been checked out
	// for longer than ResetInterval + gracePeriod, then check them back in and assume their full hourly quota
	// is available again.
	CheckInTokens(gracePeriod time.Duration) error

	// EstimateTotalRemainingCalls should return an estimate of the total count of remaining calls across all tokens
	// currently present in storage.
	EstimateTotalRemainingCalls() (int, error)
}

type backendFactory func(config interface{}) (TokenPoolStorageBackend, error)

type backendConfigFactory func() interface{}

type factory struct {
	backendFactory
	backendConfigFactory
}

var factories = make(map[string]*factory)

func registerFactory(name string, backendFactory backendFactory, configFactory backendConfigFactory) {
	factories[name] = &factory{
		backendFactory:       backendFactory,
		backendConfigFactory: configFactory,
	}
}

func NewTokenPoolStorageBackend(name string, rawConfig interface{}) (TokenPoolStorageBackend, error) {
	factory := factories[name]
	if factory == nil {
		return nil, errors.Errorf("No backend factory called %q", name)
	}

	config := factory.backendConfigFactory()

	if config != nil {
		if rawConfig == nil {
			return nil, errors.Errorf("%q backend expects a config", name)
		}

		configBytes, err := yaml.Marshal(rawConfig)
		if err != nil {
			return nil, errors.Wrapf(err, "Not a YAML config for backend %q", name)
		}

		if err := yaml.Unmarshal(configBytes, config); err != nil {
			return nil, errors.Wrapf(err, "Invalid config for backend %q", name)
		}
	}

	return factory.backendFactory(config)
}
