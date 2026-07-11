package apns

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testKeyPEM(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), key
}

func TestProviderTokenIsValidES256(t *testing.T) {
	pemKey, key := testKeyPEM(t)
	c, err := New(Config{Key: pemKey, KeyID: "KEY123", TeamID: "TEAM456", Topic: "al.basecode.harnessApp"})
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := c.providerToken()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d segments", len(parts))
	}
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if !strings.Contains(string(header), `"ES256"`) || !strings.Contains(string(header), "KEY123") {
		t.Errorf("header = %s", header)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !strings.Contains(string(claims), "TEAM456") {
		t.Errorf("claims = %s", claims)
	}
	// The signature must verify against the public key (raw R||S form).
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("sig length %d, want 64", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, sum[:], r, s) {
		t.Error("signature does not verify")
	}

	// Cached until stale — a second call returns the same token.
	again, _ := c.providerToken()
	if again != jwt {
		t.Error("token not cached")
	}
}

func TestPushSendsAlertAndSurfacesReason(t *testing.T) {
	pemKey, _ := testKeyPEM(t)
	var gotPath, gotTopic, gotAuth string
	var gotBody map[string]any
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotTopic = r.URL.Path, r.Header.Get("apns-topic")
		gotAuth = r.Header.Get("authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
		}
	}))
	defer srv.Close()

	c, err := New(Config{Key: pemKey, KeyID: "K", TeamID: "T", Topic: "al.basecode.harnessApp", Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Push(context.Background(), "devtok123", "", "Inbox: 2 things need you"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/3/device/devtok123" || gotTopic != "al.basecode.harnessApp" {
		t.Errorf("path=%q topic=%q", gotPath, gotTopic)
	}
	if !strings.HasPrefix(gotAuth, "bearer ") {
		t.Errorf("auth = %q", gotAuth)
	}
	aps := gotBody["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	if alert["body"] != "Inbox: 2 things need you" {
		t.Errorf("alert = %v", alert)
	}
	if _, hasTitle := alert["title"]; hasTitle {
		t.Error("empty title should be omitted so the banner shows the app name")
	}

	// APNs errors carry the reason so delivery can prune dead tokens.
	status = http.StatusGone
	err = c.Push(context.Background(), "devtok123", "", "hi")
	if err == nil || !strings.Contains(err.Error(), "Unregistered") {
		t.Errorf("err = %v, want Unregistered surfaced", err)
	}
}

func TestTokenStore(t *testing.T) {
	dir := t.TempDir()
	s := NewTokenStore(dir)
	if err := s.Add("aaa", "ios"); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("bbb", "ios"); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("aaa", "ios"); err != nil { // idempotent
		t.Fatal(err)
	}
	if got := s.List(); len(got) != 2 {
		t.Fatalf("want 2 tokens, got %d", len(got))
	}
	if err := s.Remove("aaa"); err != nil {
		t.Fatal(err)
	}
	got := s.List()
	if len(got) != 1 || got[0].Token != "bbb" {
		t.Errorf("after remove: %+v", got)
	}
}

func TestFromEnvUnsetMeansNotConfigured(t *testing.T) {
	t.Setenv("APNS_KEY_ID", "")
	t.Setenv("APNS_TEAM_ID", "")
	t.Setenv("APNS_TOPIC", "")
	c, err := FromEnv()
	if c != nil || err != nil {
		t.Errorf("unset env should be (nil, nil), got %v %v", c, err)
	}
}
