package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	token_pools "github.com/wk8/github-api-proxy/pkg/token-pools"
	"github.com/wk8/github-api-proxy/pkg/types"
)

const (
	defaultMaxGithubRetries = 3

	basicAuthPassword = "x-oauth-basic"
	// every time a handler uses up that many API calls, it will update the token pool
	remainingCallsReportingInterval = 50

	rateLimitHeader              = "x-ratelimit-limit"
	remainingCallsHeader         = "x-ratelimit-remaining"
	throttledRequestErrorMessage = "API rate limit exceeded"

	proxyErrorResponseBody = "unexpected proxy error"
)

type RequestHandlerI interface {
	// ProxyRequest should take any request, transparently proxy any request that's not made
	// to Github, and handle auth and throttling for requests made to Github
	ProxyRequest(request *http.Request) (response *http.Response, err error)

	// Same idea as ProxyRequest, but meant to be used as a http.Handler
	HandleRequest(writer http.ResponseWriter, request *http.Request)

	// Same as HandleRequest, but blindly re-routes all incoming requests to Github's API, regardless
	// of what host they were originally meant for.
	HandleGithubAPIRequest(writer http.ResponseWriter, request *http.Request)
}

type RequestHandler struct {
	// The transport used to perform proxy requests.
	// If nil, http.DefaultTransport is used.
	Transport http.RoundTripper

	// Github API requests that fail because of an exhausted token will be retried (with different tokens)
	// up to MaxGithubRetries times - defaults to defaultMaxGithubRetries if <= 0
	MaxGithubRetries int

	githubURL *url.URL
	tokenPool token_pools.TokenPoolStorageBackend

	currentToken *tokenInUse
	// protects access to currentToken
	currentTokenMutex sync.Mutex
}

type tokenInUse struct {
	*types.TokenSpec
	// how many API calls remain for the current token
	remainingCalls int
	// when the remaining calls count gets below this threshold, we need to report to the pool
	nextRemainingCallsReportingThreshold int

	mutex sync.Mutex
}

func NewRequestHandler(githubURL *url.URL, tokenPool token_pools.TokenPoolStorageBackend) *RequestHandler {
	return &RequestHandler{
		githubURL: githubURL,
		tokenPool: tokenPool,
	}
}

func (h *RequestHandler) ProxyRequest(request *http.Request) (response *http.Response, err error) {
	return h.proxyRequest(request, false)
}

func (h *RequestHandler) proxyRequest(request *http.Request, isKnownGithubAPIRequest bool) (response *http.Response, err error) {
	if isKnownGithubAPIRequest || h.isGithubAPIRequest(request) {
		return h.proxyGithubAPIRequest(request)
	}
	return h.makeRequest(request)
}

func (h *RequestHandler) HandleRequest(writer http.ResponseWriter, request *http.Request) {
	h.handleRequest(writer, request, false)
}

func (h *RequestHandler) HandleGithubAPIRequest(writer http.ResponseWriter, request *http.Request) {
	h.handleRequest(writer, request, true)
}

func (h *RequestHandler) handleRequest(writer http.ResponseWriter, request *http.Request, isKnownGithubAPIRequest bool) {
	response, err := h.proxyRequest(request, isKnownGithubAPIRequest)

	if err != nil {
		log.Errorf("HTTP 500 response: %v", err)

		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(proxyErrorResponseBody))
		return
	}

	headers := writer.Header()
	for key, value := range response.Header {
		headers[key] = value
	}
	writer.WriteHeader(response.StatusCode)

	if _, err := io.Copy(writer, response.Body); err != nil {
		log.Errorf("Unable to write proxyed body downstream: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		log.Errorf("Error closing HTTP response: %v", err)
	}
}

func (h *RequestHandler) makeRequest(request *http.Request) (response *http.Response, err error) {
	transport := h.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return transport.RoundTrip(request)
}

func (h *RequestHandler) isGithubAPIRequest(request *http.Request) bool {
	if h.githubURL.Host != request.Host {
		return false
	}
	return strings.HasPrefix(request.URL.Path, h.githubURL.Path)
}

// proxyGithubAPIRequest takes a HTTP request made to Github's API, inserts the right auth header,
// and takes care to update the token pool periodically
// retries up to maxRetries times if it unexpectedly gets a throttling response
// for the current token (in which case it gets a new one from the pool before retrying)
func (h *RequestHandler) proxyGithubAPIRequest(request *http.Request) (response *http.Response, err error) {
	maxRetries := h.MaxGithubRetries
	if maxRetries <= 0 {
		maxRetries = defaultMaxGithubRetries
	}

	var isThrottled bool
	for retry := 0; retry < maxRetries; retry++ {
		// Thou Shalt Not Modify The Request (https://github.com/golang/go/issues/18952)
		upstreamRequest := request.Clone(context.Background())

		upstreamRequest.Host = h.githubURL.Host
		upstreamRequest.URL.Host = h.githubURL.Host
		upstreamRequest.URL.Scheme = h.githubURL.Scheme

		if response, isThrottled, err = h.makeGithubAPIRequest(upstreamRequest); err != nil || !isThrottled {
			break
		}
	}
	return
}

func (h *RequestHandler) makeGithubAPIRequest(request *http.Request) (response *http.Response, isThrottled bool, err error) {
	var token *tokenInUse
	token, err = h.ensureToken()
	if err != nil {
		return
	}

	request.SetBasicAuth(token.Token, basicAuthPassword)

	response, err = h.makeRequest(request)
	if err != nil {
		err = errors.Wrap(err, "Unable to make HTTP request")
		return
	}

	processHeaders := shouldIncludeTokenInfoHeaders(response)
	isThrottled = isThrottledResponse(response)
	tokenExhausted := false

	func() {
		token.mutex.Lock()
		defer token.mutex.Unlock()

		if processHeaders {
			// sanity check on the rate limit
			h.checkRateLimitHeader(response, token)
			h.processRemainingCallsHeader(response, token)
		}

		tokenExhausted = isThrottled || token.remainingCalls <= 0
	}()

	if tokenExhausted {
		h.currentTokenMutex.Lock()
		if h.currentToken == token {
			h.currentToken = nil
		}
		h.currentTokenMutex.Unlock()
	}

	return
}

func (h *RequestHandler) ensureToken() (*tokenInUse, error) {
	h.currentTokenMutex.Lock()
	defer h.currentTokenMutex.Unlock()

	if h.currentToken != nil {
		return h.currentToken, nil
	}

	token, err := h.tokenPool.CheckOutToken()
	if err != nil {
		return nil, errors.Wrap(err, "Unable to check out token")
	}
	if token == nil {
		return nil, errors.New("no available API token")
	}

	h.currentToken = &tokenInUse{
		TokenSpec:      token,
		remainingCalls: token.ExpectedRateLimit,
	}
	// no need to hold that token's lock, we just created it, and no one can
	// access it until we release the handler's lock
	h.currentToken.updateNextRemainingCallsReportingThreshold()

	return h.currentToken, nil
}

func shouldIncludeTokenInfoHeaders(response *http.Response) bool {
	if response.StatusCode < http.StatusMultipleChoices || response.StatusCode >= http.StatusBadRequest {
		return true
	}
	return response.StatusCode <= http.StatusInternalServerError
}

// assumes the caller holds the token's lock
func (h *RequestHandler) checkRateLimitHeader(response *http.Response, token *tokenInUse) {
	rateLimit, err := getHeaderIntValue(response, rateLimitHeader)

	if err != nil {
		// no need to crash for this, this is just best effort
		log.Errorf("Unable to extract rate limit info from github response: %v", err)
	} else if rateLimit != token.ExpectedRateLimit {
		log.Warnf("Unexpected rate limit for token %s: expected %d, real limit is %d",
			token.redacted(), token.ExpectedRateLimit, rateLimit)

		if err := h.tokenPool.UpdateTokenRateLimit(token.Token, rateLimit); err == nil {
			token.ExpectedRateLimit = rateLimit
		} else {
			// again, just best effort
			log.Warnf("Unable to update rate limit for token %s: %v", token.redacted(), err)
		}
	}
}

// assumes the caller holds the token's lock
func (h *RequestHandler) processRemainingCallsHeader(response *http.Response, token *tokenInUse) {
	remainingCalls, err := getHeaderIntValue(response, remainingCallsHeader)

	if err == nil {
		token.remainingCalls = remainingCalls
	} else {
		// no remaining calls header in the response, let's try a best effort approach...
		log.Errorf("Unable to get remaining calls for token %s: %v", token.redacted(), err)

		token.remainingCalls--
	}

	if token.remainingCalls >= 0 && token.remainingCalls <= token.nextRemainingCallsReportingThreshold {
		if err := h.tokenPool.UpdateTokenUsage(token.Token, token.remainingCalls); err == nil {
			token.updateNextRemainingCallsReportingThreshold()
		} else {
			// best effort here too
			log.Errorf("Unable to update remaining calls for token %s: %v",
				token.redacted(), err)
		}
	}
}

// assumes that the caller holds the token's lock
func (t *tokenInUse) updateNextRemainingCallsReportingThreshold() {
	t.nextRemainingCallsReportingThreshold = t.remainingCalls - remainingCallsReportingInterval
	if t.nextRemainingCallsReportingThreshold < 0 {
		t.nextRemainingCallsReportingThreshold = 0
	}
}

// returns a redacted token, safe for logging
func (t *tokenInUse) redacted() string {
	return fmt.Sprintf("ending in %q", t.Token[len(t.Token)-6:])
}

func getHeaderIntValue(response *http.Response, key string) (int, error) {
	strValue := response.Header.Get(key)
	if strValue == "" {
		return 0, errors.Errorf("No header %q", key)
	}
	value, err := strconv.Atoi(strValue)
	if err != nil {
		return 0, errors.Wrapf(err, "Unable to convert value %q for header %q into int", strValue, key)
	}
	return value, nil
}

func isThrottledResponse(response *http.Response) bool {
	if response.StatusCode != http.StatusForbidden {
		return false
	}

	body, err := ioutil.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		log.Warnf("Unable to read response body: %v", err)
		return false
	}
	// restore for subsequent readers
	response.Body = ioutil.NopCloser(bytes.NewBuffer(body))

	var jsonBody map[string]interface{}
	if err := json.Unmarshal(body, &jsonBody); err != nil {
		log.Warnf("Unable to parse response as JSON: %v (response %q)",
			err, string(body))
	}

	message, ok := jsonBody["message"].(string)
	return ok && strings.Contains(message, throttledRequestErrorMessage)
}
