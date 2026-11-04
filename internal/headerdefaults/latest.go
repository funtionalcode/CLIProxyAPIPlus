// Package headerdefaults resolves official stable software versions for default headers.
package headerdefaults

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"
)

const npmRegistry = "https://registry.npmjs.org/"
const nodeReleases = "https://nodejs.org/dist/index.json"
const maxMetadataBytes = 2 << 20

var stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Versions contains only successfully resolved stable releases. Empty fields keep existing values.
type Versions struct {
	Claude  string
	Package string
	Runtime string
	Codex   string
}

// Fetch queries only enabled providers. Metadata requests do not carry model credentials.
func Fetch(ctx context.Context, client *http.Client, claude, codex bool) (Versions, []error) {
	var versions Versions
	var failures []error
	if client == nil {
		client = &http.Client{}
	}
	fetchNPM := func(pkg string, dst *string) {
		body, err := readMetadata(ctx, client, npmRegistry+pkg+"/latest")
		if err == nil {
			version := strings.Clone(ojson.GetBytesNoCopy(body, "version").String())
			if ojson.GetBytesNoCopy(body, "name").String() != pkg || !stableVersion.MatchString(version) {
				err = fmt.Errorf("invalid stable package metadata")
			} else {
				*dst = version
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", pkg, err))
		}
	}
	if claude {
		fetchNPM("@anthropic-ai/claude-code", &versions.Claude)
		fetchNPM("@anthropic-ai/sdk", &versions.Package)
		body, err := readMetadata(ctx, client, nodeReleases)
		if err == nil {
			for _, release := range ojson.ParseBytes(body).Array() {
				version := strings.TrimPrefix(release.Get("version").String(), "v")
				if stableVersion.MatchString(version) && newer(version, strings.TrimPrefix(versions.Runtime, "v")) {
					versions.Runtime = "v" + version
				}
			}
			if versions.Runtime == "" {
				err = fmt.Errorf("no stable Node.js release found")
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("node: %w", err))
		}
	}
	if codex {
		fetchNPM("@openai/codex", &versions.Codex)
	}
	return versions, failures
}

func readMetadata(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI")
	resp, err := client.Do(req)
	if err != nil {
		// Avoid reporting proxy credentials or other transport details in logs.
		return nil, fmt.Errorf("version metadata request failed")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debug("failed to close header version metadata response")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("version metadata returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read version metadata")
	}
	if len(body) > maxMetadataBytes || !ojson.ValidBytes(body) {
		return nil, fmt.Errorf("invalid version metadata response")
	}
	return body, nil
}

func newer(candidate, current string) bool {
	if current == "" {
		return true
	}
	a, b := strings.Split(candidate, "."), strings.Split(current, ".")
	for i := range a {
		x, errX := strconv.ParseUint(a[i], 10, 64)
		y, errY := strconv.ParseUint(b[i], 10, 64)
		if errX != nil || errY != nil {
			return false
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// UserAgentVersion preserves the configured platform and entrypoint suffix.
func UserAgentVersion(current, prefix, version, fallbackSuffix string) string {
	current = strings.TrimSpace(current)
	if strings.HasPrefix(current, prefix) {
		if i := strings.IndexAny(current[len(prefix):], " \t"); i >= 0 {
			return prefix + version + current[len(prefix)+i:]
		}
		return prefix + version
	}
	return prefix + version + fallbackSuffix
}
