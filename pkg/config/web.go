package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// AuthRequired reports whether the web UI asks for a password.
func (w WebConfig) AuthRequired() bool {
	return w.Password != "" || w.PasswordFile != ""
}

// LoadPassword returns the password in effect, reading password_file when it
// is set. An empty result means the UI runs without sign-in. A password file
// that cannot be read or is empty is an error rather than "no password":
// falling back to an open UI is the one failure this must never have.
func (w WebConfig) LoadPassword() (string, error) {
	if w.PasswordFile == "" {
		return w.Password, nil
	}
	data, err := os.ReadFile(w.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("read web.password_file: %w", err)
	}
	password := strings.TrimRight(string(data), "\r\n")
	if password == "" {
		return "", fmt.Errorf("web.password_file %q is empty", w.PasswordFile)
	}
	return password, nil
}

// ListenHost returns the host part of the listen address, or "" when the
// address names every interface or cannot be parsed.
func (w WebConfig) ListenHost() string {
	host, _, err := net.SplitHostPort(w.Listen)
	if err != nil {
		return ""
	}
	return host
}

func (w WebConfig) validate(p *problems) {
	if !w.Enabled {
		return
	}
	host, port, err := net.SplitHostPort(w.Listen)
	if err != nil {
		p.addf("web.listen %q is invalid; write host:port, such as %q", w.Listen, DefaultWebListen)
		return
	}
	if number, err := strconv.Atoi(port); err != nil || number < 0 || number > 65535 {
		p.addf("web.listen %q has an invalid port; write host:port, such as %q", w.Listen, DefaultWebListen)
	}
	if !validBasePath(w.BasePath) {
		p.addf("web.base_path %q is invalid; write a path such as \"/anvil\", or leave it empty to serve from the root", w.BasePath)
	}
	if w.Password != "" && w.PasswordFile != "" {
		p.addf("web.password and web.password_file are both set; keep only one")
	}
	if !w.AuthRequired() && !w.AllowUnauthenticated && !LoopbackHost(host) {
		p.addf("web.listen %q is reachable from other machines, but no password is set; set web.password_file (or web.password), or set web.allow_unauthenticated = true to serve the UI without sign-in", w.Listen)
	}
}

// validBasePath accepts "" and absolute paths of plain segments. The daemon
// writes the path into the page it serves, so nothing in it may need escaping
// in a URL or in HTML.
func validBasePath(base string) bool {
	if base == "" {
		return true
	}
	if !strings.HasPrefix(base, "/") {
		return false
	}
	for _, segment := range strings.Split(base[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, r := range segment {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '~':
			default:
				return false
			}
		}
	}
	return true
}

// LoopbackHost reports whether a host name or address can only be reached
// from the machine itself. An empty host means every interface.
func LoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
