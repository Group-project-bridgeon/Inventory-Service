package catalogclient

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/strictjson"
	"github.com/google/uuid"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const audience = "catalog-stock-references"
const permission = "catalog.stock-references:read"

type accessToken struct {
	Value            string
	Generation       uint64
	Expires, Refresh time.Time
}
type refreshFlight struct {
	done  chan struct{}
	token accessToken
	err   error
}
type TokenClient struct {
	cfg        Config
	http       *http.Client
	now        func() time.Time
	mu         sync.Mutex
	cached     accessToken
	next       uint64
	flight     *refreshFlight
	retryAfter time.Time
	lastErr    error
	root       context.Context
	cancel     context.CancelFunc
}

func NewTokenClient(c Config) (*TokenClient, error) {
	if c.Validate() != nil {
		return nil, ErrConfiguration
	}
	client, err := httpClient(c)
	if err != nil {
		return nil, err
	}
	if _, err = readCredential(c.SecretFile); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &TokenClient{cfg: c, http: client, now: time.Now, root: ctx, cancel: cancel}, nil
}
func (t *TokenClient) Close() { t.cancel(); t.http.CloseIdleConnections() }
func readCredential(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", ErrConfiguration
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return "", ErrConfiguration
	}
	var c struct {
		ID     string `json:"credential_id"`
		Secret string `json:"secret"`
	}
	if strictjson.Decode(data, &c) != nil {
		return "", ErrConfiguration
	}
	id, err := uuid.Parse(c.ID)
	if err != nil || id == uuid.Nil || id.String() != c.ID {
		return "", ErrConfiguration
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Secret)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != c.Secret {
		return "", ErrConfiguration
	}
	return c.ID + "." + c.Secret, nil
}
func (t *TokenClient) Invalidate(generation uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cached.Generation == generation {
		t.cached = accessToken{}
	}
}
func (t *TokenClient) Get(ctx context.Context) (accessToken, error) {
	if err := ctx.Err(); err != nil {
		return accessToken{}, err
	}
	t.mu.Lock()
	now := t.now()
	if t.cached.Value != "" && now.Before(t.cached.Refresh) {
		value := t.cached
		t.mu.Unlock()
		return value, nil
	}
	if t.flight == nil && now.Before(t.retryAfter) {
		token, err := t.cached, t.lastErr
		t.mu.Unlock()
		if transient(err) && token.Value != "" && now.Before(token.Expires) {
			return token, nil
		}
		return accessToken{}, err
	}
	flight := t.flight
	if flight == nil {
		flight = &refreshFlight{done: make(chan struct{})}
		t.flight = flight
		go t.refresh(flight)
	}
	t.mu.Unlock()
	select {
	case <-ctx.Done():
		return accessToken{}, ctx.Err()
	case <-flight.done:
		if flight.err == nil && !t.now().Before(flight.token.Expires) {
			return accessToken{}, ErrUnavailable
		}
		return flight.token, flight.err
	}
}
func (t *TokenClient) refresh(flight *refreshFlight) {
	ctx, cancel := context.WithTimeout(t.root, t.cfg.TokenTimeout)
	defer cancel()
	value, err := t.acquire(ctx)
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if err == nil {
		t.next++
		value.Generation = t.next
		t.cached = value
		t.retryAfter = time.Time{}
		t.lastErr = nil
	} else {
		t.retryAfter = now.Add(time.Second)
		t.lastErr = err
		if transient(err) && t.cached.Value != "" && now.Before(t.cached.Expires) {
			value = t.cached
			err = nil
		}
	}
	flight.token, flight.err = value, err
	t.flight = nil
	close(flight.done)
}
func readResponse(response *http.Response, limit int64) ([]byte, error) {
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrMalformed
	}
	return data, nil
}
func transient(err error) bool {
	return errors.Is(err, ErrUnavailable) || errors.Is(err, context.DeadlineExceeded)
}
func jsonResponse(response *http.Response) bool {
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	return err == nil && media == "application/json"
}
func (t *TokenClient) acquire(ctx context.Context) (accessToken, error) {
	credential, err := readCredential(t.cfg.SecretFile)
	if err != nil {
		return accessToken{}, err
	}
	start := t.now()
	body := url.Values{"grant_type": {"client_credentials"}, "audience": {audience}, "scope": {permission}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.TokenURL, strings.NewReader(body))
	if err != nil {
		return accessToken{}, ErrConfiguration
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(t.cfg.ClientID, credential)
	response, err := t.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return accessToken{}, ctx.Err()
		}
		return accessToken{}, ErrUnavailable
	}
	data, err := readResponse(response, t.cfg.TokenBodyLimit)
	if err != nil {
		if ctx.Err() != nil {
			return accessToken{}, ctx.Err()
		}
		return accessToken{}, err
	}
	switch response.StatusCode {
	case 200:
	case 400, 401, 403:
		return accessToken{}, ErrUnauthorized
	default:
		return accessToken{}, ErrUnavailable
	}
	if !jsonResponse(response) {
		return accessToken{}, ErrMalformed
	}
	var result struct {
		Token   string `json:"access_token"`
		Type    string `json:"token_type"`
		Expires int64  `json:"expires_in"`
	}
	if strictjson.Decode(data, &result) != nil || result.Type != "Bearer" || result.Expires < 1 || result.Expires > 300 || len(result.Token) > 8192 {
		return accessToken{}, ErrMalformed
	}
	parts := strings.Split(result.Token, ".")
	if len(parts) != 3 || parts[2] == "" {
		return accessToken{}, ErrMalformed
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return accessToken{}, ErrMalformed
	}
	var h struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if strictjson.Decode(header, &h) != nil || h.Alg != "RS256" || h.Typ != "JWT" {
		return accessToken{}, ErrMalformed
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return accessToken{}, ErrMalformed
	}
	var claims struct {
		Issuer      string   `json:"iss"`
		Audience    []string `json:"aud"`
		Subject     string   `json:"sub"`
		Client      string   `json:"client_id"`
		Kind        string   `json:"token_kind"`
		Permissions []string `json:"permissions"`
		JTI         string   `json:"jti"`
		IAT         int64    `json:"iat"`
		NBF         int64    `json:"nbf"`
		EXP         int64    `json:"exp"`
	}
	if strictjson.Decode(payload, &claims) != nil || claims.Issuer != t.cfg.Issuer || len(claims.Audience) != 1 || claims.Audience[0] != audience || claims.Client != t.cfg.ClientID || claims.Subject != "machine:"+claims.Client || claims.Kind != "machine" || len(claims.Permissions) != 1 || claims.Permissions[0] != permission || claims.IAT <= 0 || claims.EXP <= claims.IAT || claims.EXP-claims.IAT != result.Expires || claims.NBF != claims.IAT {
		return accessToken{}, ErrMalformed
	}
	jti, err := uuid.Parse(claims.JTI)
	if err != nil || jti == uuid.Nil || jti.String() != claims.JTI {
		return accessToken{}, ErrMalformed
	}
	now := t.now()
	if time.Unix(claims.IAT, 0).After(now.Add(30 * time.Second)) {
		return accessToken{}, ErrMalformed
	}
	expiry := time.Unix(claims.EXP, 0)
	if conservative := start.Add(time.Duration(result.Expires) * time.Second); conservative.Before(expiry) {
		expiry = conservative
	}
	if !now.Before(expiry) {
		return accessToken{}, ErrMalformed
	}
	margin := min(15*time.Second, time.Duration(result.Expires)*time.Second/5)
	return accessToken{Value: result.Token, Expires: expiry, Refresh: expiry.Add(-margin)}, nil
}
