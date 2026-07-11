// Package apns delivers push notifications to Apple devices — the harness's
// own channel to the user's phone, so scheduled runs and task results reach
// the app without Telegram in the middle. Stdlib only: the provider token is
// an ES256 JWT signed with the .p8 key from the developer portal, sent over
// HTTP/2 (Go's transport negotiates h2 with APNs automatically).
package apns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Client sends alerts through APNs with a cached provider token.
type Client struct {
	key    *ecdsa.PrivateKey
	keyID  string
	teamID string
	topic  string // the app's bundle id
	host   string // production or sandbox
	http   *http.Client

	mu       sync.Mutex
	jwt      string
	jwtBirth time.Time
}

// Config carries the four values APNs needs. Key is the raw .p8 file content
// (PEM); KeyB64 may carry the same base64-encoded for env transport.
type Config struct {
	Key    []byte
	KeyID  string
	TeamID string
	Topic  string
	Host   string // "" = production
}

// FromEnv builds a client from APNS_KEY_B64 (or APNS_KEY_FILE), APNS_KEY_ID,
// APNS_TEAM_ID, APNS_TOPIC, APNS_HOST. Returns nil (no error) when unset —
// push is simply not configured.
func FromEnv() (*Client, error) {
	keyID, teamID, topic := os.Getenv("APNS_KEY_ID"), os.Getenv("APNS_TEAM_ID"), os.Getenv("APNS_TOPIC")
	if keyID == "" || teamID == "" || topic == "" {
		return nil, nil
	}
	var key []byte
	if b64 := os.Getenv("APNS_KEY_B64"); b64 != "" {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			return nil, fmt.Errorf("apns: APNS_KEY_B64: %w", err)
		}
		key = raw
	} else if f := os.Getenv("APNS_KEY_FILE"); f != "" {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("apns: %w", err)
		}
		key = raw
	} else {
		return nil, fmt.Errorf("apns: set APNS_KEY_B64 or APNS_KEY_FILE")
	}
	return New(Config{Key: key, KeyID: keyID, TeamID: teamID, Topic: topic, Host: os.Getenv("APNS_HOST")})
}

// New builds a client from an explicit config.
func New(c Config) (*Client, error) {
	block, _ := pem.Decode(c.Key)
	if block == nil {
		return nil, fmt.Errorf("apns: key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns: parse key: %w", err)
	}
	ec, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("apns: key is not ECDSA (got %T)", parsed)
	}
	host := c.Host
	if host == "" {
		host = "https://api.push.apple.com"
	}
	return &Client{
		key: ec, keyID: c.KeyID, teamID: c.TeamID, topic: c.Topic, host: host,
		http: &http.Client{Timeout: 20 * time.Second},
	}, nil
}

// Push sends one alert to one device token. A non-nil error carries APNs's
// reason string (e.g. "BadDeviceToken", "Unregistered") so callers can prune
// dead tokens.
func (c *Client) Push(ctx context.Context, deviceToken, title, body string) error {
	jwt, err := c.providerToken()
	if err != nil {
		return err
	}
	// APNs caps payloads at 4KB; keep alerts phone-sized.
	if len(body) > 1800 {
		body = body[:1800] + "…"
	}
	alert := map[string]string{"body": body}
	if title != "" {
		alert["title"] = title // empty title → banner shows just the app name
	}
	payload, _ := json.Marshal(map[string]any{
		"aps": map[string]any{"alert": alert, "sound": "default"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.host+"/3/device/"+deviceToken, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", c.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("content-type", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	var apErr struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &apErr)
	if apErr.Reason == "" {
		apErr.Reason = strings.TrimSpace(string(raw))
	}
	return fmt.Errorf("apns: HTTP %d: %s", res.StatusCode, apErr.Reason)
}

// providerToken returns a cached ES256 JWT, reminting when older than 40
// minutes (APNs accepts 20–60; refreshing early avoids clock-skew rejects).
func (c *Client) providerToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jwt != "" && time.Since(c.jwtBirth) < 40*time.Minute {
		return c.jwt, nil
	}
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": c.keyID})
	claims, _ := json.Marshal(map[string]any{"iss": c.teamID, "iat": time.Now().Unix()})
	signing := b64(header) + "." + b64(claims)

	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, c.key, sum[:])
	if err != nil {
		return "", fmt.Errorf("apns: sign: %w", err)
	}
	// JOSE wants fixed-width raw R||S, not ASN.1.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])

	c.jwt = signing + "." + b64(sig)
	c.jwtBirth = time.Now()
	return c.jwt, nil
}
