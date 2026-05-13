// Disk-backed AWS credential cache for short-lived processes. See credcache_unix.go.
//
// s5cmd is spawned once per object-store operation by callers like
// build tooling and bulk-copy wrappers, so the SDK's in-process credential
// cache lives for milliseconds. Each fresh process resolves the IRSA chain
// independently — its own AssumeRoleWithWebIdentity call. Spawned at scale
// (e.g. many short-lived processes per CI build), the per-process resolves
// can saturate an account's AWS STS quota.
//
// Cache file path is $XDG_CACHE_HOME/anthropic/aws-cred-cache/<key>.json
// where <key> is a hash of (role_arn, role_session_name), so distinct roles
// or explicitly-set session names get separate files (no thrashing —
// botocore's ~/.aws/cli/cache/ uses the same scheme). The path and format
// can be shared with other tools that implement the same scheme.
//
// Wire format is the credential_process shape (see
// https://docs.aws.amazon.com/sdkref/latest/guide/feature-process-credentials.html)
// plus a RoleArn debuggability field. We hand back the real
// STS expiry; credentials.Expiry.SetExpiration with an expiryWindow makes the
// SDK call Retrieve() again before each request once near it (the kubelet
// rotates the JWT independently). Multipart uploads sign each part separately
// so the refresh takes effect between parts.
//
// Trust boundary: a same-UID process can read the snapshot. Pre-cache, what's
// readable is the WIF JWT — useful only with STS reach. Post-cache, the
// resolved SigV4 credential is on disk, which is a wider grant. Set
// ANTHROPIC_DISABLE_AWS_CRED_CACHE=1 in environments where that widening is
// undesirable.
//
// Any cache error — corrupt file, read-only fs, non-private file, FIFO at
// the path — is a miss, never a hard error. The build must not break on a
// cache problem.

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/peak/s5cmd/v2/log"
)

// Set non-empty to disable the disk cache and resolve per-process. Shared with
// other tools that share the cache.
const disableCredCacheEnv = "ANTHROPIC_DISABLE_AWS_CRED_CACHE"

// Reject snapshots with less than this remaining. Larger than expiryWindow so
// the disk and in-process layers agree on freshness; matches botocore's
// mandatory-refresh threshold.
const minRemaining = 10 * time.Minute

// IsExpired() flips this far before the cached creds actually die so the SDK
// calls Retrieve() again on the next request signature.
const expiryWindow = 5 * time.Minute

// The legitimate file is ~300 bytes.
const maxCredCacheFileSize = 64 * 1024

// On-disk format: standard credential_process output (the SDKs already
// understand the first five fields) plus RoleArn so a human
// reading the file knows which identity it holds. The filename hash is the
// only cache key — content is never matched against env at read time, same
// as botocore's JSONFileCache.
type cachedCreds struct {
	Version         int    `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken,omitempty"`
	// RFC 3339, server-issued. Margin is applied at read time, not baked in.
	Expiration string `json:"Expiration"`
	RoleArn    string `json:"RoleArn"`
}

// credCachePath returns the cache file path, hash-keyed on (role_arn,
// session_name). false if no cache directory is resolvable.
func credCachePath(roleArn, sessionName string) (string, bool) {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return "", false
		}
		dir = filepath.Join(home, ".cache")
	}
	key := sha256.Sum256([]byte(roleArn + "\x00" + sessionName))
	return filepath.Join(dir, "anthropic", "aws-cred-cache", hex.EncodeToString(key[:8])+".json"), true
}

// shouldDiskCache reports whether the disk cache should be active. Only when
// WIF is the resolution path. AWS_PROFILE and AWS_SHARED_CREDENTIALS_FILE are
// not gated: aws-sdk-go checks env-WIF before the shared-config providers, so
// neither can shadow the role. (The --profile and --credential-file flags can,
// and maybeWrapWithDiskCache gates on those.)
func shouldDiskCache() bool {
	return os.Getenv(disableCredCacheEnv) == "" &&
		os.Getenv("AWS_ACCESS_KEY_ID") == "" &&
		os.Getenv("AWS_ACCESS_KEY") == "" && // deprecated alias the SDK still honours
		os.Getenv("AWS_ROLE_ARN") != "" &&
		os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != ""
}

func readCredCache(path string) (v credentials.Value, expiry time.Time, ok bool) {
	f, info, err := openPrivateForRead(path)
	if err != nil {
		return
	}
	defer f.Close()
	if !isOwnedAndPrivate(info) || info.Size() > maxCredCacheFileSize {
		return
	}
	var c cachedCreds
	if json.NewDecoder(f).Decode(&c) != nil {
		return
	}
	if c.Version != 1 || c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return
	}
	expiry, err = time.Parse(time.RFC3339, c.Expiration)
	if err != nil || time.Until(expiry) < minRemaining {
		return credentials.Value{}, time.Time{}, false
	}
	return credentials.Value{
		AccessKeyID:     c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey,
		SessionToken:    c.SessionToken,
		ProviderName:    "AnthropicDiskCache",
	}, expiry, true
}

func writeCredCache(path, roleArn string, v credentials.Value, expiry time.Time) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll(0o700) is a no-op on pre-existing components. Refuse to write
	// into a directory another local user could pre-plant temp names in.
	if info, err := os.Lstat(dir); err != nil || !isOwnedPrivateDir(info) {
		return fmt.Errorf("cache dir not private: %s", dir)
	}
	body, err := json.Marshal(cachedCreds{
		Version:         1,
		AccessKeyID:     v.AccessKeyID,
		SecretAccessKey: v.SecretAccessKey,
		SessionToken:    v.SessionToken,
		Expiration:      expiry.UTC().Format(time.RFC3339),
		RoleArn:         roleArn,
	})
	if err != nil {
		return err
	}
	// O_EXCL refuses a pre-planted hard link or FIFO at the predictable tmp
	// name (mode and O_NOFOLLOW only apply at creation / for symlinks).
	// fsync-before-rename so a crash can't leave a zero-byte file.
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	f, err := openPrivateForWrite(tmp)
	if err != nil {
		return err
	}
	clean := func(e error) error { f.Close(); os.Remove(tmp); return e }
	if _, err := f.Write(body); err != nil {
		return clean(err)
	}
	if err := f.Sync(); err != nil {
		return clean(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// diskCachedProvider wraps the resolved *credentials.Credentials with a disk
// cache so concurrent and sequential short-lived processes share one resolve.
type diskCachedProvider struct {
	credentials.Expiry
	inner   *credentials.Credentials
	roleArn string
	path    string
}

func newDiskCachedProvider(inner *credentials.Credentials, roleArn, path string) *diskCachedProvider {
	return &diskCachedProvider{inner: inner, roleArn: roleArn, path: path}
}

func (p *diskCachedProvider) Retrieve() (credentials.Value, error) {
	if v, expiry, ok := readCredCache(p.path); ok {
		p.SetExpiration(expiry, expiryWindow)
		return v, nil
	}
	// Force the inner chain past its own in-process cache so a near-expiry
	// disk miss re-resolves rather than serving the inner provider's stale copy.
	p.inner.Expire()
	v, err := p.inner.Get()
	if err != nil {
		return v, err
	}
	expiry, err := p.inner.ExpiresAt()
	if err != nil {
		// Static providers (env keys) don't expire and shouldn't be cached.
		return v, nil
	}
	p.SetExpiration(expiry, expiryWindow)
	if werr := writeCredCache(p.path, p.roleArn, v, expiry); werr != nil {
		log.Debug(log.DebugMessage{Err: fmt.Sprintf("disk credential cache write failed (continuing): %v", werr)})
	}
	return v, nil
}

// maybeWrapWithDiskCache wraps the session's credential chain when the disk
// cache should be active and the chain wasn't already pinned by flags.
func maybeWrapWithDiskCache(sess *session.Session, opts Options) {
	if opts.NoSignRequest || opts.CredentialFile != "" || opts.Profile != "" || !shouldDiskCache() {
		return
	}
	roleArn := os.Getenv("AWS_ROLE_ARN")
	path, ok := credCachePath(roleArn, os.Getenv("AWS_ROLE_SESSION_NAME"))
	if !ok || sess.Config.Credentials == nil {
		return
	}
	sess.Config.Credentials = credentials.NewCredentials(
		newDiskCachedProvider(sess.Config.Credentials, roleArn, path),
	)
}
