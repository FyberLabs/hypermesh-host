package update

import (
	"strconv"
	"strings"
)

// Action is what Tick decided. reject never installs.
type Action string

const (
	ActionOff     Action = "off"
	ActionSkip    Action = "skip"
	ActionDefer   Action = "defer"
	ActionReject  Action = "reject"
	ActionInstall Action = "install"
)

// Plan is the decision for one check. Reason is safe to log.
type Plan struct {
	Action  Action
	Reason  string
	Version string
	Commit  string
}

// Manifest is release.json. It is covered by the signed checksum file.
// Build is the publisher's rising integer. ReleaseID is the blob
// directory name. The plane response does not have to repeat either.
type Manifest struct {
	Channel   string `json:"channel"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Build     int    `json:"build"`
	ReleaseID string `json:"release_id,omitempty"`
}

// Decide chooses skip, defer, reject, or install after the manifest
// has been authenticated. A candidate whose build is less than or
// equal to currentBuild is refused before the commit or semver check.
// badBuild is a build this host already rolled back; it stays skipped
// until a strictly newer build appears. Busy defers an install; it
// does not skip a real change.
func Decide(channel, currentVersion, currentCommit string, currentBuild, badBuild int, m Manifest, busy bool) Plan {
	p := Plan{Version: strings.TrimSpace(m.Version), Commit: strings.TrimSpace(m.Commit)}
	if m.Channel != channel {
		p.Action = ActionReject
		p.Reason = "release channel does not match this box"
		return p
	}
	if m.Build <= currentBuild {
		p.Action = ActionReject
		p.Reason = "build is not newer"
		return p
	}
	if badBuild > 0 && m.Build <= badBuild {
		p.Action = ActionSkip
		p.Reason = "rolled-back build"
		return p
	}
	switch channel {
	case ChannelTest:
		if p.Commit == "" {
			p.Action = ActionReject
			p.Reason = "test release has no commit"
			return p
		}
		if strings.EqualFold(p.Commit, strings.TrimSpace(currentCommit)) {
			p.Action = ActionSkip
			p.Reason = "already running this commit"
			return p
		}
	case ChannelProd:
		if !semverOK(p.Version) {
			p.Action = ActionReject
			p.Reason = "prod release version is not semver"
			return p
		}
		cur := strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
		if cur != "" && !semverOK(cur) {
			p.Action = ActionReject
			p.Reason = "running version is not semver"
			return p
		}
		if cur != "" && !semverLess(cur, p.Version) {
			p.Action = ActionSkip
			p.Reason = "already running this version or newer"
			return p
		}
	default:
		p.Action = ActionReject
		p.Reason = "channel must be test or prod"
		return p
	}
	if busy {
		p.Action = ActionDefer
		p.Reason = "lease or unacked job"
		return p
	}
	p.Action = ActionInstall
	p.Reason = "verified candidate"
	return p
}

func semverOK(s string) bool {
	_, _, _, ok := parseSemver(s)
	return ok
}

func parseSemver(s string) (int, int, int, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || len(p) > 1 && p[0] == '0' {
			return 0, 0, 0, false
		}
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return 0, 0, 0, false
		}
		n[i] = v
	}
	return n[0], n[1], n[2], true
}

// semverLess reports whether a is strictly older than b.
// Both must be semver.
func semverLess(a, b string) bool {
	am, an, ap, aok := parseSemver(a)
	bm, bn, bp, bok := parseSemver(b)
	if !aok || !bok {
		return false
	}
	if am != bm {
		return am < bm
	}
	if an != bn {
		return an < bn
	}
	return ap < bp
}
