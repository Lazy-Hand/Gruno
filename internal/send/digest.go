package send

// digest.go implements HTTP Digest access authentication (RFC 7616,
// MD5, qop=auth) as a two-round exchange: the first request goes out
// plain, a 401 with a Digest challenge gets one retry carrying the
// computed Authorization header.

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// parseDigestChallenge extracts realm/nonce/qop/algorithm/opaque from a
// WWW-Authenticate: Digest header.
func parseDigestChallenge(header string) map[string]string {
	h := strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(h), "digest") {
		return nil
	}
	out := map[string]string{}
	for _, part := range strings.Split(h[len("Digest"):], ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out
}

// digestAuthorization builds the Authorization header value.
// algorithm: MD5 or MD5-sess; qop: auth (or absent → legacy).
func digestAuthorization(method, uri, user, pass string, ch map[string]string) (string, error) {
	realm, nonce := ch["realm"], ch["nonce"]
	if nonce == "" {
		return "", fmt.Errorf("Digest 质询缺少 nonce")
	}
	alg := ch["algorithm"]
	if alg == "" {
		alg = "MD5"
	}
	if !strings.EqualFold(alg, "MD5") && !strings.EqualFold(alg, "MD5-sess") {
		return "", fmt.Errorf("不支持的 Digest 算法 %q", alg)
	}
	nc := "00000001"
	cnonceBytes := make([]byte, 8)
	rand.Read(cnonceBytes)
	cnonce := hex.EncodeToString(cnonceBytes)

	ha1sum := md5.Sum([]byte(user + ":" + realm + ":" + pass))
	ha1 := hex.EncodeToString(ha1sum[:])
	if strings.EqualFold(alg, "MD5-sess") {
		s := md5.Sum([]byte(ha1 + ":" + nonce + ":" + cnonce))
		ha1 = hex.EncodeToString(s[:])
	}
	ha2sum := md5.Sum([]byte(method + ":" + uri))
	ha2 := hex.EncodeToString(ha2sum[:])

	qop := ""
	for _, q := range strings.Split(ch["qop"], ",") {
		if strings.TrimSpace(q) == "auth" {
			qop = "auth"
			break
		}
	}
	var resp string
	if qop == "auth" {
		s := md5.Sum([]byte(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2))
		resp = hex.EncodeToString(s[:])
	} else {
		s := md5.Sum([]byte(ha1 + ":" + nonce + ":" + ha2))
		resp = hex.EncodeToString(s[:])
	}

	var b strings.Builder
	fmt.Fprintf(&b, `Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=%s`, user, realm, nonce, uri, resp, alg)
	if ch["opaque"] != "" {
		fmt.Fprintf(&b, `, opaque="%s"`, ch["opaque"])
	}
	if qop == "auth" {
		fmt.Fprintf(&b, `, qop=%s, nc=%s, cnonce="%s"`, qop, nc, cnonce)
	}
	return b.String(), nil
}

// digestRetry returns a re-issued request carrying the Digest response
// if resp is a 401 challenge, else nil.
func digestRetry(req *http.Request, resp *http.Response, a AuthSpec) (*http.Request, error) {
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, nil
	}
	var ch map[string]string
	for _, h := range resp.Header.Values("WWW-Authenticate") {
		if c := parseDigestChallenge(h); c != nil {
			ch = c
			break
		}
	}
	if ch == nil {
		return nil, nil
	}
	uri := req.URL.RequestURI()
	auth, err := digestAuthorization(req.Method, uri, a.Username, a.Password, ch)
	if err != nil {
		return nil, err
	}
	retry := req.Clone(req.Context())
	retry.Header = req.Header.Clone()
	retry.Header.Set("Authorization", auth)
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		retry.Body = body
	}
	return retry, nil
}
