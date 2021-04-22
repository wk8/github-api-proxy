package pkg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wk8/github-api-proxy/pkg/types"

	mock_token_pools "github.com/wk8/github-api-proxy/pkg/internal/mock_pkg"
)

func TestRequestHandler_ProxyRequest(t *testing.T) {
	// we need two upstream servers to hit and test our proxy against:
	// one to act as a Github API, one as another upstream
	githubUpstream := newUpstreamServer(t)
	githubUpstream.start()
	defer githubUpstream.stop()
	otherUpstream := newUpstreamServer(t)
	otherUpstream.start()
	defer otherUpstream.stop()

	t.Run("it adds tokens to Github API requests", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		dummyToken := "ohlebeautoken"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 1000,
		}, nil)

		request, err := githubUpstream.buildRequest(githubUpstream.withResponseHeaders(rateLimitHeader, "1000"))
		require.NoError(t, err)

		response, body, ok := assertHandleRequestSuccessful(t, NewRequestHandler(githubUpstream.urlStruct(), tokenPool), request, http.StatusOK)
		require.True(t, ok)

		assert.Equal(t, "pong", body)
		assertAuthHeaderMatchesToken(t, dummyToken, response.Header.Get(receivedAuthHeader))
	})

	t.Run("it updates the token pool if the token's rate limit is different than expected", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		dummyToken := "ohlebeautoken"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 100,
		}, nil)
		tokenPool.EXPECT().UpdateTokenRateLimit(dummyToken, 1000)

		request, err := githubUpstream.buildRequest(githubUpstream.withResponseHeaders(rateLimitHeader, "1000"))
		require.NoError(t, err)

		response, body, ok := assertHandleRequestSuccessful(t, NewRequestHandler(githubUpstream.urlStruct(), tokenPool), request, http.StatusOK)
		require.True(t, ok)

		assert.Equal(t, "pong", body)
		assertAuthHeaderMatchesToken(t, dummyToken, response.Header.Get(receivedAuthHeader))
	})

	t.Run("it periodically updates the token pool about the token's usage, based on the response headers", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		dummyToken := "ohlebeautoken"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 1000,
		}, nil)
		remainingCallsStart := 1000
		remainingCallsStop := 790
		for remainingCallsThreshold := remainingCallsStart - remainingCallsReportingInterval; remainingCallsThreshold > remainingCallsStop; remainingCallsThreshold -= remainingCallsReportingInterval {
			tokenPool.EXPECT().UpdateTokenUsage(dummyToken, remainingCallsThreshold)
		}

		handler := NewRequestHandler(githubUpstream.urlStruct(), tokenPool)
		for remainingCalls := remainingCallsStart; remainingCalls > remainingCallsStop; remainingCalls -= 10 {
			request, err := githubUpstream.buildRequest(githubUpstream.withResponseHeaders(remainingCallsHeader, strconv.Itoa(remainingCalls)))
			require.NoError(t, err)

			response, body, ok := assertHandleRequestSuccessful(t, handler, request, http.StatusOK)
			require.True(t, ok)

			assert.Equal(t, "pong", body)
			assertAuthHeaderMatchesToken(t, dummyToken, response.Header.Get(receivedAuthHeader))
		}
	})

	t.Run("if the response headers do not say how many calls are remaining, it does its best effort to keep track of it", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		dummyToken := "ohlebeautoken"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 111,
		}, nil)
		tokenPool.EXPECT().UpdateTokenUsage(dummyToken, 61)
		tokenPool.EXPECT().UpdateTokenUsage(dummyToken, 11)
		tokenPool.EXPECT().UpdateTokenUsage(dummyToken, 0)

		handler := NewRequestHandler(githubUpstream.urlStruct(), tokenPool)
		for i := 0; i < 111; i++ {
			request, err := githubUpstream.buildRequest()
			require.NoError(t, err)

			response, body, ok := assertHandleRequestSuccessful(t, handler, request, http.StatusOK)
			require.True(t, ok)

			assert.Equal(t, "pong", body)
			assertAuthHeaderMatchesToken(t, dummyToken, response.Header.Get(receivedAuthHeader))
		}
	})

	t.Run("when the token has been exhausted based on the response headers, it requests a new one from the backend", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		dummyToken := "ohlebeautoken"
		nextToken := "encoreplusbeau"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 10,
		}, nil)
		tokenPool.EXPECT().UpdateTokenUsage(dummyToken, 0)
		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             nextToken,
			ExpectedRateLimit: 10,
		}, nil)

		handler := NewRequestHandler(githubUpstream.urlStruct(), tokenPool)
		for remainingCalls := 10; remainingCalls > -1; remainingCalls -= 1 {
			request, err := githubUpstream.buildRequest(githubUpstream.withResponseHeaders(remainingCallsHeader, strconv.Itoa(remainingCalls)))
			require.NoError(t, err)

			response, body, ok := assertHandleRequestSuccessful(t, handler, request, http.StatusOK)
			require.True(t, ok)

			assert.Equal(t, "pong", body)
			assertAuthHeaderMatchesToken(t, dummyToken, response.Header.Get(receivedAuthHeader))
		}

		request, err := githubUpstream.buildRequest(githubUpstream.withResponseHeaders(remainingCallsHeader, "10"))
		require.NoError(t, err)

		response, body, ok := assertHandleRequestSuccessful(t, handler, request, http.StatusOK)
		require.True(t, ok)

		assert.Equal(t, "pong", body)
		assertAuthHeaderMatchesToken(t, nextToken, response.Header.Get(receivedAuthHeader))
	})

	t.Run("if the response headers do not say how many calls are remaining, it does its best effort to keep track of it and requests a new one when exhausted", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		dummyToken := "ohlebeautoken"
		nextToken := "encoreplusbeau"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 10,
		}, nil)
		tokenPool.EXPECT().UpdateTokenUsage(dummyToken, 0)
		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             nextToken,
			ExpectedRateLimit: 10,
		}, nil)

		handler := NewRequestHandler(githubUpstream.urlStruct(), tokenPool)
		for remainingCalls := 10; remainingCalls > 0; remainingCalls -= 1 {
			request, err := githubUpstream.buildRequest()
			require.NoError(t, err)

			response, body, ok := assertHandleRequestSuccessful(t, handler, request, http.StatusOK)
			require.True(t, ok)

			assert.Equal(t, "pong", body)
			assertAuthHeaderMatchesToken(t, dummyToken, response.Header.Get(receivedAuthHeader))
		}

		request, err := githubUpstream.buildRequest()
		require.NoError(t, err)

		response, body, ok := assertHandleRequestSuccessful(t, handler, request, http.StatusOK)
		require.True(t, ok)

		assert.Equal(t, "pong", body)
		assertAuthHeaderMatchesToken(t, nextToken, response.Header.Get(receivedAuthHeader))
	})

	t.Run("if a token gets unexpectedly exhausted, it retries with another token", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		dummyToken := "ohlebeautoken"
		nextToken := "encoreplusbeau"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 1000,
		}, nil)
		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             nextToken,
			ExpectedRateLimit: 1000,
		}, nil)

		githubUpstream.resetRequestsCount()
		githubUpstream.addResponse(http.StatusForbidden, `{"message": "API rate limit exceeded"}`)

		request, err := githubUpstream.buildRequest()
		require.NoError(t, err)

		response, body, ok := assertHandleRequestSuccessful(t, NewRequestHandler(githubUpstream.urlStruct(), tokenPool), request, http.StatusOK)
		require.True(t, ok)

		assert.Equal(t, "pong", body)
		assertAuthHeaderMatchesToken(t, nextToken, response.Header.Get(receivedAuthHeader))

		assert.Equal(t, uint32(2), githubUpstream.requestsCount)
	})

	t.Run("it doesn't retry failed requests that did not fail because of throttling, and does not request new tokens", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		dummyToken := "ohlebeautoken"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 1000,
		}, nil)

		githubUpstream.resetRequestsCount()

		requestHandler := NewRequestHandler(githubUpstream.urlStruct(), tokenPool)

		// wrong status code
		request, err := githubUpstream.buildRequest(githubUpstream.withResponseStatusCode(http.StatusUnauthorized))
		require.NoError(t, err)
		_, body, ok := assertHandleRequestSuccessful(t, requestHandler, request, http.StatusUnauthorized)
		require.True(t, ok)
		assert.Equal(t, "pong", body)
		assert.Equal(t, uint32(1), githubUpstream.requestsCount)

		// wrong body: not a JSON
		responseBody := "dummy error"
		request, err = githubUpstream.buildRequest(githubUpstream.withResponseStatusCode(http.StatusForbidden),
			githubUpstream.withResponseBody(responseBody))
		require.NoError(t, err)
		_, body, ok = assertHandleRequestSuccessful(t, requestHandler, request, http.StatusForbidden)
		require.True(t, ok)
		assert.Equal(t, responseBody, body)
		assert.Equal(t, uint32(2), githubUpstream.requestsCount)

		// wrong body: not the expected message
		responseBody = `{"message": "dunno"}`
		request, err = githubUpstream.buildRequest(githubUpstream.withResponseStatusCode(http.StatusForbidden),
			githubUpstream.withResponseBody(responseBody))
		require.NoError(t, err)
		_, body, ok = assertHandleRequestSuccessful(t, requestHandler, request, http.StatusForbidden)
		require.True(t, ok)
		assert.Equal(t, responseBody, body)
		assert.Equal(t, uint32(3), githubUpstream.requestsCount)
	})

	t.Run("it does not do anything to requests going to other hosts than Github's API", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		request, err := otherUpstream.buildRequest()
		require.NoError(t, err)

		otherUpstream.resetRequestsCount()

		response, body, ok := assertHandleRequestSuccessful(t, NewRequestHandler(githubUpstream.urlStruct(), tokenPool), request, http.StatusOK)
		require.True(t, ok)

		assert.Equal(t, "pong", body)
		assert.Equal(t, "", response.Header.Get(receivedAuthHeader))
		assert.Equal(t, uint32(1), otherUpstream.requestsCount)
	})

	t.Run("for requests not going to Github, it correctly copies the request and response's bodies back and forth", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		reqBody := "t'as de beaux yeux tu sais"
		request, err := otherUpstream.buildRequest(otherUpstream.withRequestPath("/echo"),
			otherUpstream.withRequestBody(reqBody))
		require.NoError(t, err)

		otherUpstream.resetRequestsCount()

		response, respBody, ok := assertHandleRequestSuccessful(t, NewRequestHandler(githubUpstream.urlStruct(), tokenPool), request, http.StatusOK)
		require.True(t, ok)

		assert.Equal(t, reqBody, respBody)
		assert.Equal(t, "", response.Header.Get(receivedAuthHeader))
		assert.Equal(t, uint32(1), otherUpstream.requestsCount)
	})

	for _, poolErrorTestCase := range []struct {
		name                        string
		poolError                   error
		expectedHandlerErrorMessage string
	}{
		{
			name:                        "if the pool doesn't have any tokens, it bubbles up the error gracefully",
			expectedHandlerErrorMessage: "no available API token",
		},
		{
			name:                        "if the pool errors out, it bubbles up the error gracefully",
			poolError:                   errors.New("dummy pool error"),
			expectedHandlerErrorMessage: "dummy pool error",
		},
	} {
		t.Run(poolErrorTestCase.name, func(t *testing.T) {
			mockController := gomock.NewController(t)
			defer mockController.Finish()
			tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

			tokenPool.EXPECT().CheckOutToken().Return(nil, poolErrorTestCase.poolError)

			request, err := githubUpstream.buildRequest()
			require.NoError(t, err)

			response, err := NewRequestHandler(githubUpstream.urlStruct(), tokenPool).ProxyRequest(request)
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), poolErrorTestCase.expectedHandlerErrorMessage)
			}
			assert.Nil(t, response)
		})
	}

	t.Run("it is thread-safe", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		dummyToken := "ohlebeautoken"
		nextToken := "encoreplusbeau"
		bothTokens := []string{dummyToken, nextToken}

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 250,
		}, nil)
		for remaining := 200; remaining >= 0; remaining -= remainingCallsReportingInterval {
			tokenPool.EXPECT().UpdateTokenUsage(dummyToken, remaining)
		}
		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             nextToken,
			ExpectedRateLimit: 250,
		}, nil)
		for remaining := 200; remaining >= remainingCallsReportingInterval; remaining -= remainingCallsReportingInterval {
			tokenPool.EXPECT().UpdateTokenUsage(nextToken, remaining)
		}

		nRequests := 490
		requests := make([]*http.Request, nRequests)
		for i := 0; i < nRequests; i++ {
			request, err := githubUpstream.buildRequest(githubUpstream.withResponseHeaders(rateLimitHeader, "250"))
			require.NoError(t, err)
			requests[i] = request
		}

		handler := NewRequestHandler(githubUpstream.urlStruct(), tokenPool)
		nThreads := 10
		var wg sync.WaitGroup
		perThread := nRequests / nThreads
		countsPerToken := make(map[string]int)
		var countsMutex sync.Mutex

		for threadID := 0; threadID < nThreads; threadID++ {
			wg.Add(1)
			go func(startIndex int) {
				defer wg.Done()
				for i := startIndex; i < startIndex+perThread; i++ {
					response, body, ok := assertHandleRequestSuccessful(t, handler, requests[i], http.StatusOK)
					require.True(t, ok)

					assert.Equal(t, "pong", body)
					token, err := extractTokenFromAuthHeader(response.Header.Get(receivedAuthHeader))
					if assert.NoError(t, err) && assert.Contains(t, bothTokens, token) {
						countsMutex.Lock()
						countsPerToken[token]++
						countsMutex.Unlock()
					}
				}
			}(threadID * perThread)
		}

		wg.Wait()
		assert.True(t, countsPerToken[dummyToken] >= 250)
		assert.True(t, countsPerToken[dummyToken] < 260)
		assert.Equal(t, nRequests, countsPerToken[dummyToken]+countsPerToken[nextToken])
	})

	t.Run("it stops using tokens when too close to their reset timestamp, based on the response headers", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		dummyToken := "ohlebeautoken"
		nextToken := "encoreplusbeau"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 1000,
		}, nil)
		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             nextToken,
			ExpectedRateLimit: 10,
		}, nil)

		handler := NewRequestHandler(githubUpstream.urlStruct(), tokenPool)

		// a fist request, that says it resets in 30 secs
		resetIn30Secs := timeNowUnix() + 30
		request, err := githubUpstream.buildRequest(githubUpstream.withResponseHeaders(resetTimestampHeader, strconv.FormatInt(resetIn30Secs, 10)))
		require.NoError(t, err)
		response, body, ok := assertHandleRequestSuccessful(t, handler, request, http.StatusOK)
		require.True(t, ok)
		assert.Equal(t, "pong", body)
		assertAuthHeaderMatchesToken(t, dummyToken, response.Header.Get(receivedAuthHeader))

		// let's make another request, that should make the handler request a new token from the pool
		request, err = githubUpstream.buildRequest()
		require.NoError(t, err)
		response, body, ok = assertHandleRequestSuccessful(t, handler, request, http.StatusOK)
		require.True(t, ok)
		assert.Equal(t, "pong", body)
		assertAuthHeaderMatchesToken(t, nextToken, response.Header.Get(receivedAuthHeader))
	})

	t.Run("if the response headers don't include a reset timestamp, it keeps track of"+
		" when the token was checked out, and stops using it when too close to the reset", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		dummyToken := "ohlebeautoken"
		nextToken := "encoreplusbeau"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 1000,
		}, nil)
		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             nextToken,
			ExpectedRateLimit: 10,
		}, nil)

		handler := NewRequestHandler(githubUpstream.urlStruct(), tokenPool)

		defer withTimeMock(t, 1000, 4540, 4540)()

		// first request
		request, err := githubUpstream.buildRequest()
		require.NoError(t, err)
		response, body, ok := assertHandleRequestSuccessful(t, handler, request, http.StatusOK)
		require.True(t, ok)
		assert.Equal(t, "pong", body)
		assertAuthHeaderMatchesToken(t, dummyToken, response.Header.Get(receivedAuthHeader))

		// second request is 59 minutes later, shouldn't use the same token
		request, err = githubUpstream.buildRequest()
		require.NoError(t, err)
		response, body, ok = assertHandleRequestSuccessful(t, handler, request, http.StatusOK)
		require.True(t, ok)
		assert.Equal(t, "pong", body)
		assertAuthHeaderMatchesToken(t, nextToken, response.Header.Get(receivedAuthHeader))
	})
}

func TestRequestHandler_HandleGithubAPIRequest(t *testing.T) {
	githubUpstream := newUpstreamServer(t)
	githubUpstream.start()
	defer githubUpstream.stop()

	t.Run("it adds tokens to Github API requests", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		handler := NewRequestHandler(githubUpstream.urlStruct(), tokenPool)

		proxy := &testHTTPServer{
			t: t,
			handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				handler.HandleGithubAPIRequest(writer, request)
			}),
		}
		proxy.start()
		defer proxy.stop()

		dummyToken := "ohlebeautoken"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 1000,
		}, nil)

		reqBody := "hey you"
		request, err := proxy.buildRequest(proxy.withRequestPath("/echo"),
			proxy.withRequestBody(reqBody))
		require.NoError(t, err)

		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)

		respBody, ok := assertResponse(t, response, http.StatusOK)
		require.True(t, ok)

		assert.Equal(t, reqBody, respBody)
		assertAuthHeaderMatchesToken(t, dummyToken, response.Header.Get(receivedAuthHeader))
	})

	t.Run("if ProxyRequest returns an error, it translates that to a 500 error", func(t *testing.T) {
		mockController := gomock.NewController(t)
		defer mockController.Finish()
		tokenPool := mock_token_pools.NewMockTokenPoolStorageBackend(mockController)

		handler := NewRequestHandler(githubUpstream.urlStruct(), tokenPool)
		handler.Transport = &failingTransport{}

		proxy := &testHTTPServer{
			t: t,
			handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				handler.HandleGithubAPIRequest(writer, request)
			}),
		}
		proxy.start()
		defer proxy.stop()

		dummyToken := "ohlebeautoken"

		tokenPool.EXPECT().CheckOutToken().Return(&types.TokenSpec{
			Token:             dummyToken,
			ExpectedRateLimit: 1000,
		}, nil)

		request, err := proxy.buildRequest()
		require.NoError(t, err)

		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)

		body, ok := assertResponse(t, response, http.StatusInternalServerError)
		require.True(t, ok)

		assert.Equal(t, proxyErrorResponseBody, body)
	})
}

// Test helpers below

type testHTTPServer struct {
	t       *testing.T
	handler http.Handler

	server        *http.Server
	port          int
	requestsCount uint32
}

func (s *testHTTPServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(writer, request)
	atomic.AddUint32(&s.requestsCount, 1)
}

func (s *testHTTPServer) resetRequestsCount() {
	s.requestsCount = 0
}

func (s *testHTTPServer) start() {
	address := findAvailableAddress(s.t)

	s.server = &http.Server{
		Addr:    address,
		Handler: s,
	}

	listeningChan := make(chan interface{})
	go func() {
		require.NoError(s.t, startHTTPServer(s.server, nil, listeningChan, ""))
	}()

	<-listeningChan

	split := strings.Split(address, ":")
	port, err := strconv.Atoi(split[len(split)-1])
	require.NoError(s.t, err)
	s.port = port
}

func (s *testHTTPServer) stop() {
	require.NoError(s.t, s.server.Shutdown(context.Background()))
}

func findAvailableAddress(t *testing.T) string {
	listener, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	return listener.Addr().String()
}

type testRequestOption func(*http.Request) error

func (s *testHTTPServer) withRequestBody(body string) testRequestOption {
	return func(request *http.Request) error {
		reader := strings.NewReader(body)
		request.Body = io.NopCloser(reader)
		request.ContentLength = int64(len(body))

		return nil
	}
}

func (s *testHTTPServer) withRequestPath(path string) testRequestOption {
	urlString := s.urlString() + path
	u, err := url.Parse(urlString)
	if err != nil {
		return func(request *http.Request) error {
			return errors.Wrapf(err, "unable to parse URL %q", urlString)
		}
	}

	return func(request *http.Request) error {
		request.URL = u
		return nil
	}
}

func (s *testHTTPServer) withQueryParam(key, value string) testRequestOption {
	return func(request *http.Request) error {
		query := request.URL.Query()
		query.Add(key, value)
		request.URL.RawQuery = query.Encode()
		return nil
	}
}

func (s *testHTTPServer) buildRequest(options ...testRequestOption) (request *http.Request, err error) {
	request, err = http.NewRequest("GET", s.urlString(), nil)
	if err != nil {
		return
	}

	for _, option := range options {
		if err = option(request); err != nil {
			return
		}
	}

	return
}

func (s *testHTTPServer) urlString() string {
	return fmt.Sprintf("http://localhost:%d", s.port)
}

func (s *testHTTPServer) urlStruct() *url.URL {
	u, err := url.Parse(s.urlString())
	require.NoError(s.t, err)
	return u
}

// to be used as an upstream server for tests
type testUpstreamServer struct {
	*testHTTPServer

	nextStatusCodes []int
	nextBodies      []string
}

const (
	receivedAuthHeader = "x-received-authorization"
	extraHeadersKey    = "extra_headers"
	statusCodeKey      = "status_code"
	bodyKey            = "body"
)

func newUpstreamServer(t *testing.T) *testUpstreamServer {
	u := &testUpstreamServer{}
	u.testHTTPServer = &testHTTPServer{
		t:       t,
		handler: u,
	}
	return u
}

func (u *testUpstreamServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	responseHeaders := writer.Header()

	if authHeader := request.Header.Get("authorization"); authHeader != "" {
		responseHeaders.Add(receivedAuthHeader, authHeader)
	}

	if extraHeadersStr := request.URL.Query().Get(extraHeadersKey); extraHeadersStr != "" {
		extraHeaders := make(map[string]string)
		require.NoError(u.t, json.Unmarshal([]byte(extraHeadersStr), &extraHeaders))
		for key, value := range extraHeaders {
			responseHeaders.Add(key, value)
		}
	}

	statusCode := http.StatusOK
	if len(u.nextStatusCodes) != 0 {
		statusCode = u.nextStatusCodes[0]
		u.nextStatusCodes = u.nextStatusCodes[1:]
	} else if statusCodeStr := request.URL.Query().Get(statusCodeKey); statusCodeStr != "" {
		var err error
		statusCode, err = strconv.Atoi(statusCodeStr)
		require.NoError(u.t, err)
	}
	writer.WriteHeader(statusCode)

	body := request.URL.Query().Get(bodyKey)
	if len(u.nextBodies) != 0 {
		body = u.nextBodies[0]
		u.nextBodies = u.nextBodies[1:]
	} else if request.URL.Path == "/echo" {
		reqBody, err := ioutil.ReadAll(request.Body)
		require.NoError(u.t, err)
		body = string(reqBody)
	}
	if body == "" {
		body = "pong"
	}

	_, err := writer.Write([]byte(body))
	require.NoError(u.t, err)
}

func (u *testUpstreamServer) addResponse(statusCode int, body string) {
	u.nextStatusCodes = append(u.nextStatusCodes, statusCode)
	u.nextBodies = append(u.nextBodies, body)
}

func (u *testUpstreamServer) withResponseHeaders(headers ...string) testRequestOption {
	if len(headers)%2 != 0 {
		return func(request *http.Request) error {
			return errors.New("extra headers must come in key-value pairs")
		}
	}

	headersMap := make(map[string]string)
	for i := 0; i < len(headers); i += 2 {
		headersMap[headers[i]] = headers[i+1]
	}

	headersJson, err := json.Marshal(headersMap)
	if err != nil {
		return func(request *http.Request) error {
			return err
		}
	}

	return u.withQueryParam(extraHeadersKey, string(headersJson))
}

func (u *testUpstreamServer) withResponseStatusCode(statusCode int) testRequestOption {
	return u.withQueryParam(statusCodeKey, strconv.Itoa(statusCode))
}

func (u *testUpstreamServer) withResponseBody(body string) testRequestOption {
	return u.withQueryParam(bodyKey, body)
}

func extractTokenFromAuthHeader(header string) (string, error) {
	b64Value := strings.TrimPrefix(header, "Basic ")
	if b64Value == header {
		return "", errors.Errorf("Expected header %q to start with Basic", header)
	}
	decoded, err := base64.StdEncoding.DecodeString(b64Value)
	if err != nil {
		return "", errors.Wrapf(err, "Unable to decode b64 string %q", b64Value)
	}
	split := strings.Split(string(decoded), ":")
	if len(split) != 2 || split[1] != basicAuthPassword {
		return "", errors.Errorf("Unexpected auth header: %q", string(decoded))
	}
	return split[0], nil
}

func assertAuthHeaderMatchesToken(t *testing.T, expectedToken, header string) bool {
	token, err := extractTokenFromAuthHeader(header)
	return assert.NoError(t, err) && assert.Equal(t, expectedToken, token)
}

func assertHandleRequestSuccessful(t *testing.T, handler *RequestHandler, request *http.Request, expectedStatusCode int) (response *http.Response, body string, success bool) {
	var err error
	response, err = handler.ProxyRequest(request)
	body, success = assertResponse(t, response, expectedStatusCode)
	success = assert.NoError(t, err) && success
	return
}

func assertResponse(t *testing.T, response *http.Response, expectedStatusCode int) (body string, success bool) {
	if !assert.NotNil(t, response) {
		return
	}
	defer func() {
		assert.NoError(t, response.Body.Close())
	}()
	if !assert.Equal(t, expectedStatusCode, response.StatusCode) {
		return
	}

	bodyBytes, err := ioutil.ReadAll(response.Body)
	if !assert.NoError(t, err) {
		return
	}

	return string(bodyBytes), true
}

var failingTransportError = errors.New("failing transport")

type failingTransport struct{}

func (f failingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, failingTransportError
}

// returns a function to cleanup
func withTimeMock(t *testing.T, times ...int64) func() {
	backup := timeNowUnix

	nextIndex := -1
	timeNowUnix = func() int64 {
		nextIndex++
		require.True(t, nextIndex < len(times))
		return times[nextIndex]
	}

	return func() {
		timeNowUnix = backup
	}
}
