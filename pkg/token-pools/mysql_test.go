package token_pools

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wk8/github-api-proxy/pkg/types"
)

func TestMysqlTokenPool(t *testing.T) {
	// this test requires to have a mysql running, so only run if a specific env var is set
	// or if we're in CI
	if os.Getenv("GITHUB_API_PROXY_MYSQL_TEST") == "" && os.Getenv("CI") == "" {
		t.Skip("Not running mysql tests, run `make dev_db_start && export GITHUB_API_PROXY_MYSQL_TEST=1` to run these")
	}

	// assumes that the mysql container started by make dev_db_start is up
	config := &mysqlTokenPoolConfig{
		Host:     "localhost",
		Port:     3306,
		User:     "root",
		Password: "password",
		DBName:   "github_api_proxy_dev",
	}

	pool, err := newMysqlTokenPoolConfig(config, true)
	assert.NoError(t, err)

	t.Run("EnsureTokens", func(t *testing.T) {
		truncateTable(t, pool)

		require.NoError(t, pool.EnsureTokensAre(generateTokenSpecs('a', 'b', 'c')...))
		tokens := requireDBContains(t, pool, []int64{'a'}, []int64{'b'}, []int64{'c'})

		tokens[0].RemainingCalls = 68
		tokens[1].RateLimit = 4000
		require.NoError(t, pool.Save(tokens[0]).Error)
		require.NoError(t, pool.Save(tokens[1]).Error)

		require.NoError(t, pool.EnsureTokensAre(generateTokenSpecs('a', 'b', 'd')...))
		requireDBContains(t, pool, []int64{'a', 68}, []int64{'b', defaultRateLimit, 0, 4000}, []int64{'d'})
	})

	t.Run("CheckOutToken with 1 available token", func(t *testing.T) {
		truncateTable(t, pool)

		defer withTimeMock(t, 1212)()

		require.NoError(t, pool.EnsureTokensAre(generateTokenSpecs('a', 'b', 'c')...))
		tokens := requireDBContains(t, pool, []int64{'a'}, []int64{'b'}, []int64{'c'})

		tokens[0].RemainingCalls = 0
		tokens[2].CheckedOutAtTimestamp = 100
		require.NoError(t, pool.Save(tokens[0]).Error)
		require.NoError(t, pool.Save(tokens[2]).Error)

		spec, err := pool.CheckOutToken()
		assert.NoError(t, err)
		if assert.NotNil(t, spec) {
			assert.Equal(t, generateTokenSpec('b'), spec)
		}

		requireDBContains(t, pool, []int64{'a', 0}, []int64{'b', defaultRateLimit, 1212}, []int64{'c', defaultRateLimit, 100})
	})

	t.Run("CheckOutToken with 3 available tokens", func(t *testing.T) {
		truncateTable(t, pool)

		require.NoError(t, pool.EnsureTokensAre(generateTokenSpecs('a', 'b', 'c')...))
		requireDBContains(t, pool, []int64{'a'}, []int64{'b'}, []int64{'c'})

		spec, err := pool.CheckOutToken()
		assert.NoError(t, err)
		if assert.NotNil(t, spec) {
			assert.Equal(t, defaultRateLimit, spec.ExpectedRateLimit)
			assert.Contains(t, []string{tokenFromChar('a'), tokenFromChar('b'), tokenFromChar('c')}, spec.Token)
		}
	})

	t.Run("CheckOutToken with no available token", func(t *testing.T) {
		truncateTable(t, pool)

		require.NoError(t, pool.EnsureTokensAre(generateTokenSpecs('a')...))
		tokens := requireDBContains(t, pool, []int64{'a'})

		tokens[0].RemainingCalls = 0
		require.NoError(t, pool.Save(tokens[0]).Error)

		spec, err := pool.CheckOutToken()
		assert.NoError(t, err)
		assert.Nil(t, spec)

		requireDBContains(t, pool, []int64{'a', 0})
	})

	t.Run("CheckOutToken with no token at all", func(t *testing.T) {
		truncateTable(t, pool)

		spec, err := pool.CheckOutToken()
		assert.NoError(t, err)
		assert.Nil(t, spec)

		requireDBContains(t, pool)
	})

	t.Run("CheckOutToken properly locks the table to ensure each worker gets a different token", func(t *testing.T) {
		truncateTable(t, pool)

		n := 26
		allChars := make([]rune, 0, n)
		for char := 'a'; char < 'a'+int32(n); char++ {
			allChars = append(allChars, char)
		}

		require.NoError(t, pool.EnsureTokensAre(generateTokenSpecs(allChars...)...))

		checkedOutTokens := make(chan *types.TokenSpec, n)
		var wg sync.WaitGroup
		wg.Add(n)
		for i := 0; i < n; i++ {
			go func() {
				token, err := pool.CheckOutToken()
				assert.NoError(t, err)
				assert.NotNil(t, token)
				checkedOutTokens <- token
				wg.Done()
			}()
		}

		expectedSpecs := make(map[string]*types.TokenSpec)
		for char := 'a'; char < 'a'+int32(n); char++ {
			spec := &types.TokenSpec{
				Token:             tokenFromChar(char),
				ExpectedRateLimit: defaultRateLimit,
			}
			expectedSpecs[spec.Token] = spec
		}

		wg.Wait()
		close(checkedOutTokens)
		for actualSpec := range checkedOutTokens {
			expectedSpec := expectedSpecs[actualSpec.Token]
			if assert.NotNil(t, expectedSpec) {
				delete(expectedSpecs, actualSpec.Token)
				assert.Equal(t, expectedSpec, actualSpec)
			}
		}
		assert.Equal(t, 0, len(expectedSpecs))
	})

	t.Run("UpdateTokenUsage on an existing token", func(t *testing.T) {
		truncateTable(t, pool)

		require.NoError(t, pool.EnsureTokensAre(generateTokenSpecs('a')...))
		requireDBContains(t, pool, []int64{'a'})

		assert.NoError(t, pool.UpdateTokenUsage(tokenFromChar('a'), 12))

		requireDBContains(t, pool, []int64{'a', 12})
	})

	t.Run("UpdateTokenUsage on an unknown token", func(t *testing.T) {
		truncateTable(t, pool)

		assert.NoError(t, pool.UpdateTokenUsage(tokenFromChar('a'), 12))

		requireDBContains(t, pool)
	})

	t.Run("UpdateTokenRateLimit on an existing token", func(t *testing.T) {
		truncateTable(t, pool)

		require.NoError(t, pool.EnsureTokensAre(generateTokenSpecs('a')...))
		requireDBContains(t, pool, []int64{'a'})

		assert.NoError(t, pool.UpdateTokenRateLimit(tokenFromChar('a'), 12))

		requireDBContains(t, pool, []int64{'a', defaultRateLimit, 0, 12})
	})

	t.Run("UpdateTokenRateLimit on an unknown token", func(t *testing.T) {
		truncateTable(t, pool)

		assert.NoError(t, pool.UpdateTokenRateLimit(tokenFromChar('a'), 12))

		requireDBContains(t, pool)
	})

	t.Run("CheckInTokens", func(t *testing.T) {
		truncateTable(t, pool)

		require.NoError(t, pool.EnsureTokensAre(generateTokenSpecs('a', 'b', 'c', 'd')...))
		tokens := requireDBContains(t, pool, []int64{'a'}, []int64{'b'}, []int64{'c'}, []int64{'d'})

		toSecs := func(duration time.Duration) int64 {
			return int64(duration.Seconds())
		}

		currentTime := 2 * time.Hour
		defer withTimeMock(t, toSecs(currentTime))()

		tokens[0].CheckedOutAtTimestamp = toSecs(currentTime - 30*time.Minute)
		tokens[1].CheckedOutAtTimestamp = toSecs(currentTime - 61*time.Minute)
		tokens[2].CheckedOutAtTimestamp = toSecs(currentTime - 90*time.Minute)
		for _, token := range tokens {
			token.RemainingCalls = 50
			require.NoError(t, pool.Save(token).Error)
		}

		assert.NoError(t, pool.CheckInTokens(time.Minute))

		requireDBContains(t, pool,
			[]int64{'a', 50, toSecs(currentTime - 30*time.Minute)},
			[]int64{'b', 50, toSecs(currentTime - 61*time.Minute)},
			[]int64{'c'},
			[]int64{'d'})
	})

	t.Run("EstimateTotalRemainingCalls with some tokens", func(t *testing.T) {
		truncateTable(t, pool)

		require.NoError(t, pool.EnsureTokensAre(generateTokenSpecs('a', 'b')...))
		tokens := requireDBContains(t, pool, []int64{'a'}, []int64{'b'})

		tokens[0].RemainingCalls = 68
		require.NoError(t, pool.Save(tokens[0]).Error)

		totalRemaining, err := pool.EstimateTotalRemainingCalls()
		assert.NoError(t, err)
		assert.Equal(t, 5068, totalRemaining)
	})

	t.Run("EstimateTotalRemainingCalls with no tokens", func(t *testing.T) {
		truncateTable(t, pool)

		totalRemaining, err := pool.EstimateTotalRemainingCalls()
		assert.NoError(t, err)
		assert.Equal(t, 0, totalRemaining)
	})
}

// Test helpers

const (
	defaultRateLimit = 5000
)

func generateTokenSpecs(chars ...rune) []*types.TokenSpec {
	specs := make([]*types.TokenSpec, len(chars))
	for i, c := range chars {
		specs[i] = generateTokenSpec(c)
	}
	return specs
}

func generateTokenSpec(char rune) *types.TokenSpec {
	return &types.TokenSpec{
		Token:             tokenFromChar(char),
		ExpectedRateLimit: defaultRateLimit,
	}
}

func tokenFromChar(c rune) string {
	return strings.Repeat(string(c), 40)
}

// see hydrateTokenShorthand below for an explanation of the input
// if successful, returns a list of the tokens in the DB in the order they were provided in expectedTokens
func assertDBContains(t *testing.T, pool *mysqlTokenPool, expectedTokens ...[]int64) []*dbToken {
	var actualTokensList []dbToken
	if !assert.NoError(t, pool.Find(&actualTokensList).Error) || !assert.Equal(t, len(expectedTokens), len(actualTokensList)) {
		return nil
	}
	actualTokens := make(map[string]dbToken)
	for _, actualToken := range actualTokensList {
		actualTokens[actualToken.Token] = actualToken
	}

	result := make([]*dbToken, len(expectedTokens))
	for i, expectedToken := range expectedTokens {
		hydrated := hydrateTokenShorthand(t, expectedToken)
		actualToken := actualTokens[hydrated.Token]
		if !assert.Equal(t, *hydrated, actualToken) {
			return nil
		}
		result[i] = &actualToken
	}

	return result
}

// same as assertDBContains, but fails the test immediately if it fails
func requireDBContains(t *testing.T, pool *mysqlTokenPool, expectedTokens ...[]int64) []*dbToken {
	tokens := assertDBContains(t, pool, expectedTokens...)
	require.NotNil(t, tokens)
	return tokens
}

// shorthand for tokens as slices of int64s.
// first one is the one character that constitutes the token,
// 2nd one is remaining calls (optional, defaults to defaultRateLimit)
// 3rd one is checked out timestamp (optional, defaults to 0)
// 4th one is the rate limit (optional, defaults to defaultRateLimit)
func hydrateTokenShorthand(t *testing.T, input []int64) *dbToken {
	assert.True(t, len(input) > 0)
	assert.True(t, len(input) < 5)

	token := &dbToken{
		Token:          tokenFromChar(rune(input[0])),
		RemainingCalls: defaultRateLimit,
		RateLimit:      defaultRateLimit,
	}
	switch len(input) {
	case 4:
		token.RateLimit = int(input[3])
		fallthrough
	case 3:
		token.CheckedOutAtTimestamp = input[2]
		fallthrough
	case 2:
		token.RemainingCalls = int(input[1])
	}

	return token
}

func truncateTable(t *testing.T, pool *mysqlTokenPool) {
	require.NoError(t, pool.Where("1 = 1").Delete(&dbToken{}).Error)
	requireDBContains(t, pool)
}

// returns a function to cleanup
func withTimeMock(t *testing.T, times ...int64) func() {
	backup := timeNowUnix

	index := -1
	timeNowUnix = func() int64 {
		index++
		require.True(t, index < len(times))
		return times[index]
	}

	return func() {
		timeNowUnix = backup
	}
}
