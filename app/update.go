package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The only network access of the program: asking GitHub for the latest
// release, when the user asks to check for updates (TZ.md "Network")

// releasesURL is the API address of the latest release
var releasesURL = "https://api.github.com/repos/ipoluianov/altesp/releases/latest"

// Release is the latest release
type Release struct {
	Tag   string // "v0.3.1"
	URL   string // its page
	Newer bool   // newer than this copy
}

// LatestRelease asks GitHub for the latest release and compares it with
// Version
func LatestRelease(ctx context.Context) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", releasesURL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "altesp/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var r struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Release{}, err
	}
	return Release{Tag: r.TagName, URL: r.HTMLURL, Newer: newer(r.TagName, Version)}, nil
}

// newer tells whether the version tag a is newer than b ("v1.2.10" >
// "v1.2.9"); a development build ("dev", "v1.2-3-gabc") counts as older
// than any release
func newer(a, b string) bool {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	if !oka {
		return false
	}
	if !okb {
		return true
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var p [3]int
	v = strings.TrimPrefix(v, "v")
	if strings.Contains(v, "-") || v == "" {
		return p, false
	}
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return p, false
	}
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil {
			return p, false
		}
		p[i] = n
	}
	return p, true
}

// SetReleasesURL asks another address for the latest release (the tests
// of the window); restore puts the address back
func SetReleasesURL(url string) (restore func()) {
	old := releasesURL
	releasesURL = url
	return func() { releasesURL = old }
}
