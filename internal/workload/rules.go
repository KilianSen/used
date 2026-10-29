package workload

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

// Rule statically marks process trees as workload. All set fields must match;
// a match covers the process and all its descendants.
//
//	[[ignore]]
//	cgroup = "/kubepods.slice/**"
//
//	[[ignore]]
//	path = "/opt/farm/bin/*"
//	tag  = "farm"
type Rule struct {
	Tag    string `toml:"tag" json:"tag,omitempty"`
	Name   string `toml:"name" json:"name,omitempty"`     // process name glob (spoofable!)
	Path   string `toml:"path" json:"path,omitempty"`     // executable path glob
	Cgroup string `toml:"cgroup" json:"cgroup,omitempty"` // Linux cgroup v2 path glob
}

type rule struct {
	tag                string
	name, path, cgroup *regexp.Regexp
}

func compile(r Rule) (rule, error) {
	c := rule{tag: r.Tag}
	var err error
	for _, f := range []struct {
		pat string
		dst **regexp.Regexp
	}{{r.Name, &c.name}, {r.Path, &c.path}, {r.Cgroup, &c.cgroup}} {
		if f.pat == "" {
			continue
		}
		if *f.dst, err = glob(f.pat); err != nil {
			return c, fmt.Errorf("ignore rule %q: %w", f.pat, err)
		}
		if c.tag == "" {
			c.tag = "rule:" + f.pat
		}
	}
	if c.name == nil && c.path == nil && c.cgroup == nil {
		return c, fmt.Errorf("ignore rule needs name, path or cgroup")
	}
	return c, nil
}

func (r rule) match(p Proc) bool {
	ok := func(re *regexp.Regexp, v func() string) bool { return re == nil || re.MatchString(v()) }
	return ok(r.name, func() string { return p.Name }) &&
		ok(r.cgroup, p.cgroup) &&
		ok(r.path, p.exe) // last: exe lookup is the expensive one
}

// glob: `**` crosses separators, `*` doesn't, `?` is one char.
// Case-insensitive where the file system usually is.
func glob(pat string) (*regexp.Regexp, error) {
	q := regexp.QuoteMeta(pat)
	q = strings.ReplaceAll(q, `\*\*`, "\x00")
	q = strings.ReplaceAll(q, `\*`, `[^/\\]*`)
	q = strings.ReplaceAll(q, `\?`, `.`)
	q = strings.ReplaceAll(q, "\x00", `.*`)
	if runtime.GOOS != "linux" {
		q = "(?i)" + q
	}
	return regexp.Compile("^" + q + "$")
}
