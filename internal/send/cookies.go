package send

// The cookie jar sends see: Set-Cookie responses are kept and sent back
// to matching requests for the life of the app, as Bruno does. The
// standard jar cannot list what it holds, so this one remembers the
// origins that set cookies.

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"

	"golang.org/x/net/publicsuffix"
)

// CookieJar is an http.CookieJar that can list and clear its cookies.
type CookieJar struct {
	mu      sync.Mutex
	jar     *cookiejar.Jar
	origins map[string]*url.URL // scheme://host that set cookies
	// paths records, per host, the cookie paths in use, so Delete can
	// expire a cookie set on a path other than "/".
	paths map[string]map[string]bool
}

// NewCookieJar returns an empty jar with public-suffix rules, so a site
// cannot set cookies for a whole TLD.
func NewCookieJar() *CookieJar {
	j, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	return &CookieJar{jar: j, origins: map[string]*url.URL{}, paths: map[string]map[string]bool{}}
}

func (c *CookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jar.SetCookies(u, cookies)
	o := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}
	c.origins[o.String()] = o
	host := strings.ToLower(u.Host)
	if c.paths[host] == nil {
		c.paths[host] = map[string]bool{}
	}
	for _, ck := range cookies {
		if ck.MaxAge < 0 {
			continue // an expiry, not a new path
		}
		path := ck.Path
		if path == "" {
			path = "/"
		}
		c.paths[host][path] = true
	}
}

func (c *CookieJar) Cookies(u *url.URL) []*http.Cookie {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.jar.Cookies(u)
}

// StoredCookie is one cookie as the jar would send it to Host.
type StoredCookie struct {
	Host  string
	Name  string
	Value string
}

// List returns the cookies the jar sends to each origin that set any,
// sorted by host and name.
func (c *CookieJar) List() []StoredCookie {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []StoredCookie
	seen := map[string]bool{}
	for _, o := range c.origins {
		for _, ck := range c.jar.Cookies(o) {
			k := o.Host + "\x00" + ck.Name
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, StoredCookie{Host: o.Host, Name: ck.Name, Value: ck.Value})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Delete expires the cookie name as host's requests see it, on the
// paths it may have been set for.
func (c *CookieJar) Delete(host, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(host)
	paths := c.paths[key]
	if len(paths) == 0 {
		paths = map[string]bool{"/": true}
	}
	for _, o := range c.origins {
		if !strings.EqualFold(o.Host, host) {
			continue
		}
		for path := range paths {
			c.jar.SetCookies(o, []*http.Cookie{{Name: name, Path: path, MaxAge: -1}})
		}
	}
	delete(c.paths, key)
}

// Clear forgets every cookie.
func (c *CookieJar) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jar, _ = cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	c.origins = map[string]*url.URL{}
	c.paths = map[string]map[string]bool{}
}
