package token_pools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInMemoryTokenPool(t *testing.T) {
	t.Run("Checkout with randomization", func(t *testing.T) {
		nCalls := 100
		counts := make(map[string]int)

		nTokens := 10
		allChars := make([]rune, 0, nTokens)
		for char := 'a'; char < 'a'+int32(nTokens); char++ {
			allChars = append(allChars, char)
		}

		for i := 0; i < nCalls; i++ {
			pool := newInMemoryTokenPool(true)

			require.NoError(t, pool.EnsureTokensAre(generateTokenSpecs(allChars...)...))

			token, err := pool.CheckOutToken()
			require.NoError(t, err)
			counts[token.Token]++
		}

		totalCount := 0
		for char := 'a'; char < 'a'+int32(nTokens); char++ {
			totalCount += counts[tokenFromChar(char)]
		}

		require.Equal(t, nCalls, totalCount)
		require.Less(t, 1, len(counts))
	})
}
