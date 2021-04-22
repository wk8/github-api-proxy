package token_pools

import (
	"fmt"
	"time"

	"github.com/pkg/errors"
	mysqldriver "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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
			return newMysqlTokenPoolConfig(config, false)
		},
		func() interface{} {
			return &mysqlTokenPoolConfig{
				Port: 3306,
			}
		},
	)
}

type mysqlTokenPool struct {
	*gorm.DB
}

var (
	_ TokenPoolStorageBackend = &mysqlTokenPool{}

	// easier to mock, for tests
	timeNowUnix = func() int64 {
		return time.Now().Unix()
	}
)

type mysqlTokenPoolConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"db_name"`
}

type dbToken struct {
	Token                 string `gorm:"primarykey;type:varchar(40);not null"`
	RateLimit             int
	RemainingCalls        int   `gorm:"index:checked_out_remaining_calls_idx,priority:2"`
	CheckedOutAtTimestamp int64 `gorm:"index:checked_out_remaining_calls_idx,priority:1"`
}

// debug prints debug statements for queries; comes in handy when debugging
func newMysqlTokenPoolConfig(config *mysqlTokenPoolConfig, debug bool) (*mysqlTokenPool, error) {
	// see https://github.com/go-sql-driver/mysql#dsn-data-source-name
	dbDSN := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8&parseTime=True&loc=Local",
		config.User, config.Password, config.Host, config.Port, config.DBName)
	db, err := gorm.Open(mysqldriver.Open(dbDSN), &gorm.Config{})
	if err != nil {
		return nil, errors.Wrapf(err, "unable to connect to mysql DB")
	}

	if debug {
		db = db.Debug()
	}

	if err := db.AutoMigrate(&dbToken{}); err != nil {
		return nil, errors.Wrapf(err, "unable to migrate mysql tables")
	}

	return &mysqlTokenPool{
		DB: db,
	}, nil
}

func (m *mysqlTokenPool) EnsureTokensAre(tokens ...*types.TokenSpec) error {
	tokenStrings := make([]string, len(tokens))
	for i, token := range tokens {
		tokenStrings[i] = token.Token
	}

	return m.withTransaction(func(transaction *gorm.DB) error {
		transaction = transaction.Clauses(clause.OnConflict{DoNothing: true})

		if err := transaction.Not(tokenStrings).Delete(&dbToken{}).Error; err != nil {
			return errors.Wrap(err, "unable to delete obsolete tokens")
		}

		newTokens := make([]*dbToken, len(tokens))
		for i, token := range tokens {
			newTokens[i] = &dbToken{
				Token:          token.Token,
				RateLimit:      token.ExpectedRateLimit,
				RemainingCalls: token.ExpectedRateLimit,
			}
		}

		return errors.Wrap(transaction.Create(newTokens).Error, "unable to create new tokens")
	})
}

func (m *mysqlTokenPool) CheckOutToken() (spec *types.TokenSpec, err error) {
	err = m.withTableLock(func(transaction *gorm.DB) error {
		var token dbToken

		query := transaction.Where("remaining_calls > 0 AND checked_out_at_timestamp = 0").First(&token)
		if err := query.Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return errors.Wrap(err, "unable to query tokens")
		}

		token.CheckedOutAtTimestamp = timeNowUnix()
		if err := transaction.Save(&token).Error; err != nil {
			return errors.Wrap(err, "unable to update token")
		}

		spec = &types.TokenSpec{
			Token:             token.Token,
			ExpectedRateLimit: token.RateLimit,
		}

		return nil
	})
	return
}

func (m *mysqlTokenPool) UpdateTokenUsage(token string, remaining int) error {
	return m.Model(&dbToken{}).Where("token = ?", token).Update("remaining_calls", remaining).Error
}

func (m *mysqlTokenPool) UpdateTokenRateLimit(token string, rateLimit int) error {
	return m.Model(&dbToken{}).Where("token = ?", token).Update("rate_limit", rateLimit).Error
}

func (m *mysqlTokenPool) CheckInTokens(gracePeriod time.Duration) error {
	cutoff := timeNowUnix() - int64((ResetInterval + gracePeriod).Seconds())
	return m.Model(&dbToken{}).Where("checked_out_at_timestamp < ?", cutoff).Updates(map[string]interface{}{
		"remaining_calls":          gorm.Expr("rate_limit"),
		"checked_out_at_timestamp": 0,
	}).Error
}

func (m *mysqlTokenPool) EstimateTotalRemainingCalls() (int, error) {
	sum := 0
	return sum, m.Model(&dbToken{}).Select("COALESCE(SUM(remaining_calls), 0)").First(&sum).Error
}

func (m *mysqlTokenPool) withTransaction(f func(transaction *gorm.DB) error) error {
	transaction := m.Begin()

	if err := f(transaction); err != nil {
		transaction.Rollback()
		return err
	}

	return errors.Wrap(transaction.Commit().Error, "unable to commit")
}

func (m *mysqlTokenPool) withTableLock(f func(transaction *gorm.DB) error) error {
	return m.withTransaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec("LOCK TABLE db_tokens WRITE").Error; err != nil {
			return errors.Wrap(err, "unable to lock table")
		}
		unlock := func() error {
			return errors.Wrap(transaction.Exec("UNLOCK TABLES").Error, "unable to unlock table")
		}
		defer func() {
			if err := recover(); err != nil {
				// best effort
				_ = unlock()
				panic(err)
			}
		}()

		returnErr := f(transaction)
		unlockErr := unlock()

		if returnErr != nil {
			return returnErr
		}
		return unlockErr
	})
}
