//go:build !windows

package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws/credentials"
)

// tmpCache returns a path inside a 0700 dir so writeCredCache's parent-privacy
// check passes (t.TempDir() roots are 0755).
func tmpCache(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "c")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "creds.json")
}

func mustWrite(t *testing.T, path, roleArn string, remaining time.Duration) {
	t.Helper()
	v := credentials.Value{AccessKeyID: "AKIA-T", SecretAccessKey: "SEC", SessionToken: "TOK"}
	if err := writeCredCache(path, roleArn, v, time.Now().Add(remaining)); err != nil {
		t.Fatal(err)
	}
}

func TestCredCacheRoundTrip(t *testing.T) {
	path := tmpCache(t)
	mustWrite(t, path, "arn:role/a", time.Hour)
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}
	got, expiry, ok := readCredCache(path)
	if !ok || got.AccessKeyID != "AKIA-T" || got.SessionToken != "TOK" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	if time.Until(expiry) < 59*time.Minute {
		t.Fatalf("expiry too short: %v", time.Until(expiry))
	}
}

func TestCredCacheRejectsNearExpiry(t *testing.T) {
	path := tmpCache(t)
	mustWrite(t, path, "arn:r", minRemaining-time.Second)
	if _, _, ok := readCredCache(path); ok {
		t.Fatal("near-expiry should miss")
	}
}

func TestCredCacheRejectsNonPrivateFileAndDir(t *testing.T) {
	path := tmpCache(t)
	mustWrite(t, path, "arn:r", time.Hour)
	if _, _, ok := readCredCache(path); !ok {
		t.Fatal("private file should hit")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readCredCache(path); ok {
		t.Fatal("non-private file should miss")
	}
	// World-writable dir → write refused.
	if err := os.Chmod(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := writeCredCache(path, "arn:r", credentials.Value{AccessKeyID: "k"}, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("writable dir should refuse write")
	}
}

func TestCredCacheCorruptOrOversizedFileIsAMiss(t *testing.T) {
	path := tmpCache(t)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readCredCache(path); ok {
		t.Fatal("corrupt file should miss")
	}
	if err := os.WriteFile(path, make([]byte, maxCredCacheFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readCredCache(path); ok {
		t.Fatal("oversized file should miss")
	}
}

func TestCredCachePathIsKeyedOnRoleAndSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, _ := credCachePath("arn:role/a", "")
	b, _ := credCachePath("arn:role/b", "")
	a2, _ := credCachePath("arn:role/a", "explicit")
	if a == b || a == a2 {
		t.Fatalf("paths should differ: %q %q %q", a, b, a2)
	}
	if got, _ := credCachePath("arn:role/a", ""); got != a {
		t.Fatal("path should be deterministic")
	}
}

func TestShouldDiskCache(t *testing.T) {
	t.Setenv("AWS_ROLE_ARN", "arn:r")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "/tok")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_ACCESS_KEY", "")
	t.Setenv(disableCredCacheEnv, "")
	if !shouldDiskCache() {
		t.Fatal("WIF env set, should cache")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA")
	if shouldDiskCache() {
		t.Fatal("static creds, should not cache")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_ACCESS_KEY", "AKIA")
	if shouldDiskCache() {
		t.Fatal("legacy alias static creds, should not cache")
	}
	t.Setenv("AWS_ACCESS_KEY", "")
	t.Setenv(disableCredCacheEnv, "1")
	if shouldDiskCache() {
		t.Fatal("opt-out, should not cache")
	}
}

func TestCredCacheWritesCredentialProcessShape(t *testing.T) {
	// Cross-tool contract: other implementations may read/write the same field
	// names. Go's json.Decode is case-insensitive so RoundTrip wouldn't catch
	// a casing typo; this test holds the wire contract.
	path := tmpCache(t)
	mustWrite(t, path, "arn:r", time.Hour)
	data, _ := os.ReadFile(path)
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"Version", "AccessKeyId", "SecretAccessKey", "SessionToken", "Expiration", "RoleArn"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("missing %s in %v", k, body)
		}
	}
}

type errProvider struct{}

func (errProvider) Retrieve() (credentials.Value, error) {
	return credentials.Value{}, errors.New("boom")
}
func (errProvider) IsExpired() bool { return true }

func TestDiskCachedProviderShortCircuitsInner(t *testing.T) {
	path := tmpCache(t)
	mustWrite(t, path, "arn:r", time.Hour)
	p := newDiskCachedProvider(credentials.NewCredentials(errProvider{}), "arn:r", path)
	got, err := p.Retrieve()
	if err != nil || got.AccessKeyID != "AKIA-T" {
		t.Fatalf("expected cache hit: %v %+v", err, got)
	}
	if p.IsExpired() {
		t.Fatal("freshly cached creds should not be expired")
	}
}

type expiringProvider struct {
	credentials.Expiry
	calls int
}

func (p *expiringProvider) Retrieve() (credentials.Value, error) {
	p.calls++
	p.SetExpiration(time.Now().Add(time.Hour), 0) // window 0: production WIF chain leaves ExpiryWindow unset so ExpiresAt() returns the raw STS expiry
	return credentials.Value{AccessKeyID: "AKIA-INNER", SecretAccessKey: "S", SessionToken: "T"}, nil
}

func TestDiskCachedProviderResolvesAndWritesOnMiss(t *testing.T) {
	path := tmpCache(t)
	ep := &expiringProvider{}
	p := newDiskCachedProvider(credentials.NewCredentials(ep), "arn:r", path)
	got, err := p.Retrieve()
	if err != nil || got.AccessKeyID != "AKIA-INNER" || ep.calls != 1 {
		t.Fatalf("got %+v err=%v calls=%d", got, err, ep.calls)
	}
	if _, _, ok := readCredCache(path); !ok {
		t.Fatal("expected cache file written on miss")
	}
}

func TestDiskCachedProviderRefreshesNearExpiry(t *testing.T) {
	// The mid-transfer refresh path: a near-expiry disk cache must trigger a
	// fresh resolve and overwrite the snapshot with the new expiry.
	path := tmpCache(t)
	mustWrite(t, path, "arn:r", minRemaining-time.Second)
	ep := &expiringProvider{}
	p := newDiskCachedProvider(credentials.NewCredentials(ep), "arn:r", path)
	if _, err := p.Retrieve(); err != nil {
		t.Fatal(err)
	}
	if ep.calls != 1 {
		t.Fatalf("near-expiry cache should trigger a resolve, got %d calls", ep.calls)
	}
	if _, expiry, ok := readCredCache(path); !ok || time.Until(expiry) < 59*time.Minute {
		t.Fatal("expected cache refreshed with new expiry")
	}
}
