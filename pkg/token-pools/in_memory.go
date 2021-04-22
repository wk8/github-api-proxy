package token_pools

import (
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
		func(config interface{}) (TokenPoolStorageBackend, error) {
			if config != nil {
				return nil, errors.Errorf("memory pool tokens don't accept any config")
			}
			return newTokenPoolMemoryStorage(), nil
		},
		func() interface{} {
			return nil
		},
	)
}

// inMemoryTokenPool is an in-memory, local implementation of TokenPoolStorageBackend
type inMemoryTokenPool struct {
	tokens map[string]*tokenWithRemainingCount

	// each token in data appears exactly once in either of these 2 maps, at all times
	// checkedInTokens is simply used as a set
	checkedInTokens map[string]bool
	// checkedOutTokens maps tokens to when they were checked out (`time.Time`s)
	checkedOutTokens *orderedmap.OrderedMap

	mutex sync.Mutex
}

var _ TokenPoolStorageBackend = &inMemoryTokenPool{}

type tokenWithRemainingCount struct {
	*types.TokenSpec
	remaining int
}

func newTokenPoolMemoryStorage() *inMemoryTokenPool {
	return &inMemoryTokenPool{
		tokens:           make(map[string]*tokenWithRemainingCount),
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
			t.tokens[tokenSpec.Token] = &tokenWithRemainingCount{
				TokenSpec: tokenSpec,
				remaining: tokenSpec.ExpectedRateLimit,
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

func (t *inMemoryTokenPool) CheckOutToken() (*types.TokenSpec, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	token := ""
	for checkedInToken := range t.checkedInTokens {
		token = checkedInToken
		break
	}
	if token == "" {
		return nil, nil
	}

	delete(t.checkedInTokens, token)
	t.checkedOutTokens.Set(token, time.Now())

	return t.tokens[token].TokenSpec, nil
}

func (t *inMemoryTokenPool) UpdateTokenUsage(token string, remaining int) error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if withCount := t.tokens[token]; withCount != nil {
		withCount.remaining = remaining
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
		t.tokens[token].remaining = t.tokens[token].ExpectedRateLimit
	}

	return nil
}

func (t *inMemoryTokenPool) EstimateTotalRemainingCalls() (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	total := 0
	for _, withCount := range t.tokens {
		total += withCount.remaining
	}
	return total, nil
}
