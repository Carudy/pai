package cli

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func canonicalServeOrigin(raw string) (string, error) {
	invalid := errors.New("must be an http/https origin without path, query, fragment, or userinfo")
	u, err := url.Parse(raw)
	if err != nil {
		return "", invalid
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" || strings.ContainsAny(raw, "?#") {
		return "", invalid
	}
	hostname := u.Hostname()
	if hostname == "" {
		return "", invalid
	}

	if net.ParseIP(hostname) == nil {
		if strings.ContainsAny(u.Host, "[]") {
			return "", invalid
		}
		for _, label := range strings.Split(hostname, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", invalid
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return "", invalid
				}
			}
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", invalid
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", invalid
		}
	}
	// Reject unbracketed IPv6 and other authority spellings browsers cannot use.
	host := strings.ToLower(hostname)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	if !strings.EqualFold(u.Host, host) {
		return "", invalid
	}
	return u.Scheme + "://" + host, nil
}

func serveToken(f serveFlags) (string, error) {
	token := os.Getenv("PAI_SERVE_TOKEN")
	if f.tokenFile != "" {
		data, err := os.ReadFile(f.tokenFile)
		if err != nil {
			return "", fmt.Errorf("read token file: %w", err)
		}
		token = string(data)
	}
	token = strings.TrimSpace(token)
	if !utf8.ValidString(token) || strings.IndexFunc(token, unicode.IsSpace) >= 0 || strings.IndexFunc(token, unicode.IsControl) >= 0 {
		return "", errors.New("token must contain valid non-whitespace characters")
	}
	ip := net.ParseIP(f.ip)
	if ip == nil {
		return "", errors.New("bind address must be an IP literal")
	}
	if (!ip.IsLoopback() || f.publicOrigin != "") && utf8.RuneCountInString(token) < 32 {
		return "", errors.New("non-loopback bind or public-origin requires a token of at least 32 characters")
	}
	return token, nil
}
