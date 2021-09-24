package token_pools

import (
	"math/rand"
	"sync"
	"time"

	"github.com/pkg/errors"
	orderedmap "github.com/wk8/go-ordered-map"

	"github.com/wk8/github-api-proxy/pkg/types"
)

const memory = "memory"

func init() {
	registerFactory(
		memory,
		func(rawConfig interface{}) (TokenPoolStorageBackend, error) {
			randomize := false
			if rawConfig != nil {
				config, ok := rawConfig.(*inMemoryTokenPoolConfig)
				if !ok {
					return nil, errors.Errorf("not a valid in-memory pool config")
				}
				randomize = config.Randomize
			}

			return newInMemoryTokenPool(randomize), nil
		},
		func() interface{} {
			return &inMemoryTokenPoolConfig{}
		},
	)
}

// inMemoryTokenPool is an in-memory, local implementation of TokenPoolStorageBackend
type inMemoryTokenPool struct {
	randomize bool

	tokens map[string]*types.Token

	// each token in `tokens` appears exactly once in either of these 2 maps, at all times
	// checkedInTokens is simply used as a set
	checkedInTokens map[string]bool
	// checkedOutTokens maps tokens to when they were checked out (`time.Time`s)
	checkedOutTokens *orderedmap.OrderedMap

	mutex sync.Mutex
}

var _ TokenPoolStorageBackend = &inMemoryTokenPool{}

type inMemoryTokenPoolConfig struct {
	// randomization allows for less conflicts when running this proxy
	// as a distributed service but still using in memory token pools
	// - but distributed deployments really should be distributed pools!
	Randomize bool `yaml:"randomize"`
}

func newInMemoryTokenPool(randomize bool) *inMemoryTokenPool {
	return &inMemoryTokenPool{
		randomize:        randomize,
		tokens:           make(map[string]*types.Token),
		checkedInTokens:  make(map[string]bool),
		checkedOutTokens: orderedmap.New(),
	}
}

func (t *inMemoryTokenPool) EnsureTokensAre(tokens ...*types.TokenSpec) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	knownTokens := make(map[string]bool)

	for _, tokenSpec := range tokens {
		if t.tokens[tokenSpec.Token] == nil {
			t.tokens[tokenSpec.Token] = &types.Token{
				TokenSpec:      *tokenSpec,
				RemainingCalls: tokenSpec.ExpectedRateLimit,
			}

			t.checkedInTokens[tokenSpec.Token] = true
		}

		knownTokens[tokenSpec.Token] = true
	}

	for token := range t.tokens {
		if !knownTokens[token] {
			delete(t.tokens, token)
			delete(t.checkedInTokens, token)
			t.checkedOutTokens.Delete(token)
		}
	}

	return nil
}

func (t *inMemoryTokenPool) CheckOutToken() (*types.Token, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	token := ""

	if len(t.checkedInTokens) != 0 {
		// we have checked-in tokens
		randIndex := 0
		if t.randomize {
			randIndex = rand.Int() % len(t.checkedInTokens)
		}

		i := 0
		for checkedInToken := range t.checkedInTokens {
			if i == randIndex {
				token = checkedInToken
				break
			}
			i++
		}

		delete(t.checkedInTokens, token)
		t.checkedOutTokens.Set(token, time.Now())
	} else {
		// no checked-in token - if we have any checked-out tokens,
		// select the one with the most calls remaining, if any
		currentMax := 0

		for pair := t.checkedOutTokens.Oldest(); pair != nil; pair = pair.Next() {
			checkedOutToken := pair.Key.(string)
			if t.tokens[checkedOutToken].RemainingCalls > currentMax {
				token = checkedOutToken
			}
		}
	}

	if token == "" {
		return nil, nil
	}

	return t.tokens[token], nil
}

func (t *inMemoryTokenPool) UpdateTokenUsage(token string, remaining int) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if withCount := t.tokens[token]; withCount != nil {
		withCount.RemainingCalls = remaining
	}
	return nil
}

func (t *inMemoryTokenPool) UpdateTokenRateLimit(token string, rateLimit int) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if withCount := t.tokens[token]; withCount != nil {
		withCount.ExpectedRateLimit = rateLimit
	}
	return nil
}

func (t *inMemoryTokenPool) CheckInTokens(gracePeriod time.Duration) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	cutoff := time.Now().Add(-(ResetInterval + gracePeriod))
	tokensToCheckIn := make([]string, 0)

	for pair := t.checkedOutTokens.Oldest(); pair != nil; pair = pair.Next() {
		checkedOutAt := pair.Value.(time.Time)

		if checkedOutAt.Before(cutoff) {
			tokensToCheckIn = append(tokensToCheckIn, pair.Key.(string))
		} else {
			break
		}
	}

	for _, token := range tokensToCheckIn {
		t.checkedOutTokens.Delete(token)
		t.checkedInTokens[token] = true
		t.tokens[token].RemainingCalls = t.tokens[token].ExpectedRateLimit
	}

	return nil
}

func (t *inMemoryTokenPool) EstimateTotalRemainingCalls() (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	total := 0
	for _, withCount := range t.tokens {
		total += withCount.RemainingCalls
	}
	return total, nil
}
