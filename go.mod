module github.com/wk8/github-api-proxy

go 1.16

replace github.com/kr/mitm => github.com/wk8/mitm v0.0.0-20180423001252-44941974427c

require (
	github.com/golang/mock v1.5.0
	github.com/jessevdk/go-flags v1.5.0
	github.com/kr/mitm v0.0.0-00010101000000-000000000000
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.8.1
	github.com/stretchr/testify v1.7.0
	github.com/wk8/go-ordered-map v0.2.0
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b
	gorm.io/driver/mysql v1.0.5
	gorm.io/gorm v1.21.8
)
