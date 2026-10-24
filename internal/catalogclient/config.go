package catalogclient

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrConfiguration = errors.New("invalid stock reference integration configuration")
var ErrUnauthorized = stock.ErrReferenceUnauthorized
var ErrDenied = stock.ErrReferenceDenied
var ErrMalformed = stock.ErrReferenceMalformed
var ErrUnavailable = stock.ErrReferenceUnavailable

type Config struct {
	Enabled                                                  bool
	TokenURL, BatchURL, ClientID, SecretFile, CAFile, Issuer string
	TokenTimeout, ReferenceTimeout                           time.Duration
	TokenBodyLimit, ReferenceBodyLimit                       int64
}

func LoadConfig() (Config, error) {
	c := Config{TokenTimeout: 2 * time.Second, ReferenceTimeout: 2 * time.Second, TokenBodyLimit: 16 << 10, ReferenceBodyLimit: 1 << 20}
	switch os.Getenv("STOCK_REFERENCE_ENABLED") {
	case "", "false":
		return c, nil
	case "true":
		c.Enabled = true
	default:
		return Config{}, ErrConfiguration
	}
	c.TokenURL = os.Getenv("STOCK_AUTH_TOKEN_URL")
	c.BatchURL = os.Getenv("STOCK_CATALOG_BATCH_URL")
	c.ClientID = os.Getenv("STOCK_MACHINE_ID")
	c.SecretFile = os.Getenv("STOCK_CREDENTIAL_SECRET_FILE")
	c.CAFile = os.Getenv("STOCK_CA_FILE")
	c.Issuer = os.Getenv("STOCK_AUTH_ISSUER")
	for key, target := range map[string]*time.Duration{"STOCK_TOKEN_TIMEOUT": &c.TokenTimeout, "STOCK_REFERENCE_TIMEOUT": &c.ReferenceTimeout} {
		if raw := os.Getenv(key); raw != "" {
			v, err := time.ParseDuration(raw)
			if err != nil || v <= 0 || v > 5*time.Second {
				return Config{}, ErrConfiguration
			}
			*target = v
		}
	}
	for key, target := range map[string]*int64{"STOCK_TOKEN_MAX_BYTES": &c.TokenBodyLimit, "STOCK_REFERENCE_MAX_BYTES": &c.ReferenceBodyLimit} {
		if raw := os.Getenv(key); raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return Config{}, ErrConfiguration
			}
			*target = v
		}
	}
	return c, c.Validate()
}
func validURL(raw string, path string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Path == path && u.RawPath == ""
}
func (c Config) Validate() error {
	id, e1 := uuid.Parse(c.ClientID)
	if !validURL(c.TokenURL, "/api/v1/auth/service-token") || !validURL(c.BatchURL, "/api/v1/catalog/stock-references/batch") || e1 != nil || id == uuid.Nil || id.String() != c.ClientID || c.SecretFile == "" || c.Issuer == "" || strings.TrimSpace(c.Issuer) != c.Issuer || c.TokenTimeout <= 0 || c.TokenTimeout > 5*time.Second || c.ReferenceTimeout <= 0 || c.ReferenceTimeout > 5*time.Second || c.TokenBodyLimit < 1024 || c.TokenBodyLimit > 64<<10 || c.ReferenceBodyLimit < 1024 || c.ReferenceBodyLimit > 2<<20 {
		return ErrConfiguration
	}
	return nil
}
func httpClient(c Config) (*http.Client, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if c.CAFile != "" {
		file, err := os.Open(c.CAFile)
		if err != nil {
			return nil, ErrConfiguration
		}
		pem, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		_ = file.Close()
		if err != nil || len(pem) > 1<<20 || !roots.AppendCertsFromPEM(pem) {
			return nil, ErrConfiguration
		}
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10, MaxConnsPerHost: 8, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second, DisableCompression: true}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
