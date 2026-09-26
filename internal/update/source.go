package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Artifact is one file the plane named. URL may be a SAS URL.
// Do not log it. Size is the byte cap and the expected length.
type Artifact struct {
	Name   string
	SHA256 string
	Size   int64
	URL    string
}

// Candidate is the plane's description of the channel head.
// The signed release.json is what the agent installs from, after it
// matches this description.
type Candidate struct {
	Channel   string
	Version   string
	Commit    string
	Artifacts []Artifact
}

// UpdateSource is where a channel head comes from.
// Latest returns nil, nil when the plane has nothing on the channel.
// Fetch downloads one artifact URL and stops at size bytes.
type UpdateSource interface {
	Latest(ctx context.Context, channel string) (*Candidate, error)
	Fetch(ctx context.Context, rawURL string, size int64) ([]byte, error)
}

// Source is the production UpdateSource. LatestFn calls the control
// plane. Fetch downloads the SAS URL with no device credential.
type Source struct {
	LatestFn func(context.Context, string) (*Candidate, error)
	Client   *http.Client
}

func (s Source) Latest(ctx context.Context, channel string) (*Candidate, error) {
	if s.LatestFn == nil {
		return nil, fmt.Errorf("hypermesh update: source is required")
	}
	return s.LatestFn(ctx, channel)
}

func (s Source) Fetch(ctx context.Context, rawURL string, size int64) ([]byte, error) {
	return FetchLimited(ctx, s.Client, rawURL, size)
}

var queryRE = regexp.MustCompile(`\?[^\s"'<>]*`)

// RedactQuery strips URL query strings. A SAS URL's query is the
// credential. Use this on any string that might be logged.
func RedactQuery(s string) string {
	return queryRE.ReplaceAllString(s, "")
}

// FetchLimited downloads rawURL over HTTPS and requires exactly size
// bytes. The read stops at size+1. Errors name the path without the
// query string and do not include the response body.
func FetchLimited(ctx context.Context, client *http.Client, rawURL string, size int64) ([]byte, error) {
	if size <= 0 || size > maxAsset {
		return nil, fmt.Errorf("%w: artifact size", ErrRejected)
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("%w: artifact url", ErrRejected)
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL == nil || req.URL.Scheme != "https" || req.URL.User != nil || len(via) >= 2 {
			return fmt.Errorf("hypermesh update: redirect rejected")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: artifact url", ErrRejected)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hypermesh update: download %s failed", RedactQuery(rawURL))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("hypermesh update: download %s status %d", RedactQuery(rawURL), resp.StatusCode)
	}
	if resp.ContentLength > size {
		return nil, fmt.Errorf("%w: artifact larger than manifest size", ErrRejected)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, size+1))
	if err != nil {
		return nil, fmt.Errorf("hypermesh update: download %s failed", RedactQuery(rawURL))
	}
	if int64(len(body)) != size {
		return nil, fmt.Errorf("%w: downloaded size does not match the manifest", ErrRejected)
	}
	return body, nil
}

func artifactByName(c *Candidate, name string) (Artifact, error) {
	var found *Artifact
	for i := range c.Artifacts {
		if c.Artifacts[i].Name != name {
			continue
		}
		if found != nil {
			return Artifact{}, fmt.Errorf("%w: duplicate artifact %s", ErrRejected, name)
		}
		found = &c.Artifacts[i]
	}
	if found == nil {
		return Artifact{}, fmt.Errorf("%w: release has no %s", ErrRejected, name)
	}
	return *found, nil
}

func signatureArtifact(c *Candidate) (Artifact, error) {
	sig, sigErr := artifactByName(c, SignatureName)
	arm, armErr := artifactByName(c, SignatureArmorName)
	switch {
	case sigErr == nil:
		return sig, nil
	case armErr == nil:
		return arm, nil
	default:
		return Artifact{}, fmt.Errorf("%w: release has no checksum signature", ErrRejected)
	}
}

func scrub(err error) error {
	if err == nil {
		return nil
	}
	msg := RedactQuery(err.Error())
	if msg == err.Error() {
		return err
	}
	return fmt.Errorf("%s", msg)
}

func sha256Hex(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
