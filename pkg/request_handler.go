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
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/wk8/github-api-proxy/pkg/internal"
	token_pools "github.com/wk8/github-api-proxy/pkg/token-pools"
	"github.com/wk8/github-api-proxy/pkg/types"
)

const (
	defaultMaxGithubRetries = 25

	basicAuthPassword = "x-oauth-basic"
	// every time a handler uses up that many API calls, it will update the token pool
	remainingCallsReportingInterval = 50

	rateLimitHeader              = "x-ratelimit-limit"
	remainingCallsHeader         = "x-ratelimit-remaining"
	resetTimestampHeader         = "x-ratelimit-reset"
	throttledRequestErrorMessage = "API rate limit exceeded"

	proxyErrorResponseBody = "unexpected proxy error"
)

// we don't use token this close to their reset timestamp, to allow for some clock drift between servers
var resetIntervalErrorMarginSeconds = int64((60 * time.Second).Seconds())

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
	*types.Token
	// when the remaining calls count gets below this threshold, we need to report to the pool
	nextRemainingCallsReportingThreshold int
	// once past the reset, we need to stop using this token, let the pool check it back in on its own,
	// and ask for another one from the pool
	resetTimestamp int64

	mutex sync.Mutex
}

func NewRequestHandler(githubURL *url.URL, tokenPool token_pools.TokenPoolStorageBackend) *RequestHandler {
	return &RequestHandler{
		githubURL: githubURL,
		tokenPool: tokenPool,
	}
}

func (h *RequestHandler) ProxyRequest(request *http.Request) (response *http.Response, err error) {
	log.Debugf("Proxy-ing %s request to %v", request.Method, request.URL)

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
	log.Debugf("Handling %s request to %v", request.Method, request.URL)

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

	if err == nil && isThrottled {
		err = errors.Errorf("still throttled after %d attempts", maxRetries)
	}
	if err != nil {
		log.Errorf("Unable to proxy %s Github request to %v: %v", request.Method, request.URL, err)
	}

	return
}

func (h *RequestHandler) makeGithubAPIRequest(request *http.Request) (response *http.Response, isThrottled bool, err error) {
	var token *tokenInUse
	token, err = h.ensureToken()
	if err != nil {
		return
	}

	request.SetBasicAuth(token.Token.Token, basicAuthPassword)

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
			h.processRateLimitHeader(response, token)
			h.processRemainingCallsHeader(response, token)
			h.processResetTimestampHeader(response, token)
		}

		tokenExhausted = isThrottled || token.RemainingCalls <= 0
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
		if internal.TimeNowUnix() >= h.currentToken.resetTimestamp-resetIntervalErrorMarginSeconds {
			// can't use this token any more, the pool will check it back in shortly
			h.currentToken = nil
		} else {
			return h.currentToken, nil
		}
	}

	token, err := h.tokenPool.CheckOutToken()
	if err != nil {
		return nil, errors.Wrap(err, "Unable to check out token")
	}
	if token == nil {
		return nil, errors.New("no available API token")
	}

	h.currentToken = &tokenInUse{
		Token:          token,
		resetTimestamp: internal.TimeNowUnix() + int64(token_pools.ResetInterval.Seconds()),
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
func (h *RequestHandler) processRateLimitHeader(response *http.Response, token *tokenInUse) {
	rateLimit64, err := getHeaderIntValue(response, rateLimitHeader)
	rateLimit := int(rateLimit64)

	if err != nil {
		// no need to crash for this, this is just best effort
		log.Errorf("Unable to extract rate limit info from github response: %v", err)
	} else if rateLimit != token.ExpectedRateLimit {
		log.Warnf("Unexpected rate limit for token %s: expected %d, real limit is %d",
			token.redacted(), token.ExpectedRateLimit, rateLimit)

		if err := h.tokenPool.UpdateTokenRateLimit(token.Token.Token, rateLimit); err == nil {
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
		token.RemainingCalls = int(remainingCalls)
	} else {
		// no remaining calls header in the response, let's try a best effort approach...
		log.Errorf("Unable to get remaining calls for token %s: %v", token.redacted(), err)

		token.RemainingCalls--
	}

	if token.RemainingCalls >= 0 && token.RemainingCalls <= token.nextRemainingCallsReportingThreshold {
		if err := h.tokenPool.UpdateTokenUsage(token.Token.Token, token.RemainingCalls); err == nil {
			token.updateNextRemainingCallsReportingThreshold()
		} else {
			// best effort here too
			log.Errorf("Unable to update remaining calls for token %s: %v",
				token.redacted(), err)
		}
	}
}

// assumes the caller holds the token's lock
func (h *RequestHandler) processResetTimestampHeader(response *http.Response, token *tokenInUse) {
	resetTimestamp, err := getHeaderIntValue(response, resetTimestampHeader)
	if err == nil {
		if token.resetTimestamp > resetTimestamp {
			token.resetTimestamp = resetTimestamp
		}
	}
}

// assumes that the caller holds the token's lock
func (t *tokenInUse) updateNextRemainingCallsReportingThreshold() {
	t.nextRemainingCallsReportingThreshold = t.RemainingCalls - remainingCallsReportingInterval
	if t.nextRemainingCallsReportingThreshold < 0 {
		t.nextRemainingCallsReportingThreshold = 0
	}
}

// returns a redacted token, safe for logging
func (t *tokenInUse) redacted() string {
	return fmt.Sprintf("ending in %q", t.Token.Token[len(t.Token.Token)-6:])
}

func getHeaderIntValue(response *http.Response, key string) (int64, error) {
	strValue := response.Header.Get(key)
	if strValue == "" {
		return 0, errors.Errorf("No header %q", key)
	}
	value, err := strconv.ParseInt(strValue, 10, 64)
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
