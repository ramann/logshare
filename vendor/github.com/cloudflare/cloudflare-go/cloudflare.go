// Package cloudflare implements the Cloudflare v4 API.
package cloudflare

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/pkg/errors"
)

const apiURL = "https://api.cloudflare.com/client/v4"
const (
	AuthApiToken = 1 << iota
	// AuthKeyEmail specifies that we should authenticate with API key and email address
	AuthKeyEmail // = 1 << iota
	// AuthUserService specifies that we should authenticate with a User-Service key
	AuthUserService
)

// API holds the configuration for the current API client. A client should not
// be modified concurrently.
type API struct {
	APIToken	  string
	APIKey            string
	APIEmail          string
	APIUserServiceKey string
	BaseURL           string
	organizationID    string
	headers           http.Header
	httpClient        *http.Client
	authType          int
}

// New creates a new Cloudflare v4 API client.
func New(token, key, email string, opts ...Option) (*API, error) {
	if token == "" && ( key == "" || email == "") {
		return nil, errors.New(errEmptyCredentials)
	}

	api := &API{
		APIToken: token,
		APIKey:   key,
		APIEmail: email,
		BaseURL:  apiURL,
		headers:  make(http.Header),
		authType: AuthKeyEmail,
	}

	err := api.parseOptions(opts...)
	if err != nil {
		return nil, errors.Wrap(err, "options parsing failed")
	}

	// Fall back to http.DefaultClient if the package user does not provide
	// their own.
	if api.httpClient == nil {
		api.httpClient = http.DefaultClient
	}

	return api, nil
}

// SetAuthType sets the authentication method (AuthyKeyEmail or AuthUserService or AuthApiToken).
func (api *API) SetAuthType(authType int) {
	api.authType = authType
}

// ZoneIDByName retrieves a zone's ID from the name.
func (api *API) ZoneIDByName(zoneName string) (string, error) {
	res, err := api.ListZones(zoneName)
	if err != nil {
		return "", errors.Wrap(err, "ListZones command failed")
	}
	for _, zone := range res {
		if zone.Name == zoneName {
			return zone.ID, nil
		}
	}
	return "", errors.New("Zone could not be found")
}

// makeRequest makes a HTTP request and returns the body as a byte slice,
// closing it before returnng. params will be serialized to JSON.
func (api *API) makeRequest(method, uri string, params interface{}) ([]byte, error) {
	return api.makeRequestWithAuthType(method, uri, params, api.authType)
}

func (api *API) makeRequestWithAuthType(method, uri string, params interface{}, authType int) ([]byte, error) {
	// Replace nil with a JSON object if needed
	var reqBody io.Reader
	if params != nil {
		json, err := json.Marshal(params)
		if err != nil {
			return nil, errors.Wrap(err, "error marshalling params to JSON")
		}
		reqBody = bytes.NewReader(json)
	} else {
		reqBody = nil
	}

	resp, err := api.request(method, uri, reqBody, authType)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "could not read response body")
	}

	switch resp.StatusCode {
	case http.StatusOK:
		break
	case http.StatusUnauthorized:
		return nil, errors.Errorf("HTTP status %d: invalid credentials", resp.StatusCode)
	case http.StatusForbidden:
		return nil, errors.Errorf("HTTP status %d: insufficient permissions", resp.StatusCode)
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout,
		522, 523, 524:
		return nil, errors.Errorf("HTTP status %d: service failure", resp.StatusCode)
	default:
		var s string
		if body != nil {
			s = string(body)
		}
		return nil, errors.Errorf("HTTP status %d: content %q", resp.StatusCode, s)
	}

	return body, nil
}

// request makes a HTTP request to the given API endpoint, returning the raw
// *http.Response, or an error if one occurred. The caller is responsible for
// closing the response body.
func (api *API) request(method, uri string, reqBody io.Reader, authType int) (*http.Response, error) {
	req, err := http.NewRequest(method, api.BaseURL+uri, reqBody)
	if err != nil {
		return nil, errors.Wrap(err, "HTTP request creation failed")
	}

	// Apply any user-defined headers first.
	req.Header = cloneHeader(api.headers)
	if authType&AuthApiToken != 0 {
		req.Header.Set("Authorization", "Bearer "+api.APIToken)
	}
	if authType&AuthKeyEmail != 0 {
		req.Header.Set("X-Auth-Key", api.APIKey)
		req.Header.Set("X-Auth-Email", api.APIEmail)
	}
	if authType&AuthUserService != 0 {
		req.Header.Set("X-Auth-User-Service-Key", api.APIUserServiceKey)
	}

	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := api.httpClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "HTTP request failed")
	}

	return resp, nil
}

// Returns the base URL to use for API endpoints that exist for both accounts and organizations.
// If an Organization option was used when creating the API instance, returns the org URL.
//
// accountBase is the base URL for endpoints referring to the current user. It exists as a
// parameter because it is not consistent across APIs.
func (api *API) userBaseURL(accountBase string) string {
	if api.organizationID != "" {
		return "/organizations/" + api.organizationID
	}
	return accountBase
}

// cloneHeader returns a shallow copy of the header.
// copied from https://godoc.org/github.com/golang/gddo/httputil/header#Copy
func cloneHeader(header http.Header) http.Header {
	h := make(http.Header)
	for k, vs := range header {
		h[k] = vs
	}
	return h
}

// ResponseInfo contains a code and message returned by the API as errors or
// informational messages inside the response.
type ResponseInfo struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Response is a template.  There will also be a result struct.  There will be a
// unique response type for each response, which will include this type.
type Response struct {
	Success  bool           `json:"success"`
	Errors   []ResponseInfo `json:"errors"`
	Messages []ResponseInfo `json:"messages"`
}

// ResultInfo contains metadata about the Response.
type ResultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalPages int `json:"total_pages"`
	Count      int `json:"count"`
	Total      int `json:"total_count"`
}

// RawResponse keeps the result as JSON form
type RawResponse struct {
	Response
	Result json.RawMessage `json:"result"`
}

// Raw makes a HTTP request with user provided params and returns the
// result as untouched JSON.
func (api *API) Raw(method, endpoint string, data interface{}) (json.RawMessage, error) {
	res, err := api.makeRequest(method, endpoint, data)
	if err != nil {
		return nil, errors.Wrap(err, errMakeRequestError)
	}

	var r RawResponse
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, errors.Wrap(err, errUnmarshalError)
	}
	return r.Result, nil
}
