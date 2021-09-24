package types

type TokenSpec struct {
	Token string `yaml:"token"`
	// ExpectedRateLimit is the expected hourly limit for this token (e.g. 5000 for a user token, 60 for an anonymous one)
	ExpectedRateLimit int `yaml:"expected_rate_limit"`
}

type Token struct {
	TokenSpec
	RemainingCalls int
}

type TLSConfig struct {
	CrtPath string `yaml:"crt_path"`
	KeyPath string `yaml:"key_path"`
}
