[![Build Status](https://travis-ci.com/wk8/github-api-proxy.svg?branch=master)](https://travis-ci.com/wk8/github-api-proxy)

# github-api-proxy

A proxy to talk to Github's API.

## Why?

Github throttles API calls very aggressively. This simple proxy allows using a pool of tokens to draw from to allow your services to talk to Github without having to worry about which tokens to use.

## Usage

This proxy can be used one of two ways:
* either as an explicit proxy, i.e. by pointing your services to it instead of Github
* or as an implicit proxy, i.e. by configuring your services to use it as a HTTP proxy (in which case you might have to make them trust a self-signed certificate authority in order to be able to man-in-the-middle Github API requests)


Note that you can also configure this proxy to do both at the same time on different ports.

Example config file for an explicit proxy:
```yaml
log_level: info

token_specs:
  default_rate_limit: 5000
  specs:
    - token: token_1
    - token: token_2
    - token: token_3
    - token: token_4

token_pool:
  # in-memory backend, only relevant for single-instance deployments
  backend: memory

explicit_proxy:
  # explicit proxy on port 8081
  port: 8081
```

Example config for implicit proxy:
```yaml
log_level: info

token_specs:
  default_rate_limit: 5000
  specs:
    - token: token_1
    - token: token_2
    - token: token_3
    - token: token_4

token_pool:
  backend: mysql
  config:
    host: my.db
    port: 3306
    user: user
    password: pwd
    db_name: github_api_proxy # must already exist

mitm_proxy:
  port: 8082
  tls_config:
    crt_path: /path/to/ca-cert.pem
    key_path: /path/to/ca-key.pem
```
