package executor

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var (
	codexCLIUserAgentVersionPattern = regexp.MustCompile(`\bcodex_cli_rs/(\d+)\.(\d+)\.(\d+)`)

	codexLatestUserAgentVersionMu sync.RWMutex
	codexLatestUserAgentVersion   = codexCLIUserAgentVersion{major: 0, minor: 118, patch: 0}
)

type codexCLIUserAgentVersion struct {
	major int
	minor int
	patch int
}

func (v codexCLIUserAgentVersion) Compare(other codexCLIUserAgentVersion) int {
	switch {
	case v.major != other.major:
		if v.major > other.major {
			return 1
		}
		return -1
	case v.minor != other.minor:
		if v.minor > other.minor {
			return 1
		}
		return -1
	case v.patch != other.patch:
		if v.patch > other.patch {
			return 1
		}
		return -1
	default:
		return 0
	}
}

func (v codexCLIUserAgentVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

func parseCodexCLIUserAgentVersion(userAgent string) (codexCLIUserAgentVersion, bool) {
	matches := codexCLIUserAgentVersionPattern.FindStringSubmatch(strings.TrimSpace(userAgent))
	if len(matches) != 4 {
		return codexCLIUserAgentVersion{}, false
	}
	major, errMajor := strconv.Atoi(matches[1])
	if errMajor != nil {
		return codexCLIUserAgentVersion{}, false
	}
	minor, errMinor := strconv.Atoi(matches[2])
	if errMinor != nil {
		return codexCLIUserAgentVersion{}, false
	}
	patch, errPatch := strconv.Atoi(matches[3])
	if errPatch != nil {
		return codexCLIUserAgentVersion{}, false
	}
	return codexCLIUserAgentVersion{major: major, minor: minor, patch: patch}, true
}

func observeCodexUserAgentVersion(userAgent string) {
	version, ok := parseCodexCLIUserAgentVersion(userAgent)
	if !ok {
		return
	}

	codexLatestUserAgentVersionMu.Lock()
	if version.Compare(codexLatestUserAgentVersion) > 0 {
		codexLatestUserAgentVersion = version
	}
	codexLatestUserAgentVersionMu.Unlock()
}

func codexFixedMacUserAgent(headers http.Header, configUserAgent string) string {
	observeCodexUserAgentVersion(configUserAgent)
	if headers != nil {
		observeCodexUserAgentVersion(headers.Get("User-Agent"))
	}

	codexLatestUserAgentVersionMu.RLock()
	version := codexLatestUserAgentVersion
	codexLatestUserAgentVersionMu.RUnlock()

	return fmt.Sprintf("codex_cli_rs/%s (Mac OS 26.3.1; arm64) iTerm.app/3.6.9", version.String())
}

func resetCodexFixedMacUserAgentForTest() {
	codexLatestUserAgentVersionMu.Lock()
	codexLatestUserAgentVersion = codexCLIUserAgentVersion{major: 0, minor: 118, patch: 0}
	codexLatestUserAgentVersionMu.Unlock()
}
