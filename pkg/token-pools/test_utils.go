package token_pools

import (
	"strings"

	"github.com/wk8/github-api-proxy/pkg/types"
)

const defaultRateLimit = 5000

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
