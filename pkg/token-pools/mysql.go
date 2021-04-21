package token_pools

import (
	"time"

	"github.com/pkg/errors"

	"github.com/wk8/github-api-proxy/pkg/types"
)

const mysql = "mysql"

func init() {
	registerFactory(
		mysql,
		func(rawConfig interface{}) (TokenPoolStorageBackend, error) {
			config, ok := rawConfig.(*mysqlTokenPoolConfig)
			if !ok {
				return nil, errors.Errorf("not a mysql config")
			}
			return NewMysqlTokenPoolConfig(config), nil
		},
		func() interface{} {
			return &mysqlTokenPoolConfig{
				Port: 3306,
			}
		},
	)
}

// TODO wkpo make private? or else make the config puyblic, but that makes little sense
type MysqlTokenPool struct {
	// TODO wkpo
}

var _ TokenPoolStorageBackend = &MysqlTokenPool{}

type mysqlTokenPoolConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

func NewMysqlTokenPoolConfig(config *mysqlTokenPoolConfig) *MysqlTokenPool {
	return &MysqlTokenPool{
		// TODO wkpo
	}
}

func (m MysqlTokenPool) EnsureTokensAre(tokens ...*types.TokenSpec) error {
	panic("TODO wkpo")
}

func (m MysqlTokenPool) CheckOutToken() (*types.TokenSpec, error) {
	panic("TODO wkpo")
}

func (m MysqlTokenPool) UpdateTokenUsage(token string, remaining int) error {
	panic("TODO wkpo")
}

func (m MysqlTokenPool) UpdateTokenRateLimit(token string, rateLimit int) error {
	panic("TODO wkpo")
}

func (m MysqlTokenPool) CheckInTokens(gracePeriod time.Duration) error {
	panic("TODO wkpo")
}

func (m MysqlTokenPool) EstimateTotalRemainingCalls() (int, error) {
	panic("TODO wkpo")
}
