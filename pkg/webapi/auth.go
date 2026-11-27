package webapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie = "anvil_session"
	// sessionLifetime is how long a sign-in lasts. Sessions live in memory,
	// so a daemon restart ends them all earlier.
	sessionLifetime = 30 * 24 * time.Hour
	// maxSessions bounds the session table. Signing in past it drops the
	// session closest to expiry.
	maxSessions = 256

	// A client may fail freeAttempts sign-ins before attempts are spaced by a
	// fixed delay. Attempts made during that delay do not extend it, so a peer
	// cannot keep a correct password locked out indefinitely.
	freeAttempts = 5
	loginDelay   = 2 * time.Second
	// maxThrottled bounds the table of clients being throttled.
	maxThrottled = 1024
)

// auth holds the shared password, the sessions it opened, and the sign-in
// throttle. The zero value means "no password": every request is allowed.
type auth struct {
	mu       sync.Mutex
	password string
	sessions map[string]time.Time
	failures map[string]*failure
}

type failure struct {
	count       int
	nextAttempt time.Time
	last        time.Time
}

// setPassword installs the password in effect. A changed password ends every
// session: whoever is signed in proved knowledge of a password that no longer
// opens anything.
func (a *auth) setPassword(password string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if password == a.password {
		return
	}
	a.password = password
	a.sessions = nil
	a.failures = nil
}

func (a *auth) required() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.password != ""
}

// authenticated reports whether the request carries a live session, or needs
// none.
func (a *auth) authenticated(r *http.Request, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.password == "" {
		return true
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	expires, found := a.sessions[cookie.Value]
	if !found {
		return false
	}
	if !expires.After(now) {
		delete(a.sessions, cookie.Value)
		return false
	}
	return true
}

// login checks a password and opens a session. wait is how long the client
// has to hold off when it is being throttled; the password is not looked at
// then, so a locked-out client learns nothing from trying.
func (a *auth) login(client, password string, now time.Time) (session string, wait time.Duration, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if record := a.failures[client]; record != nil && record.nextAttempt.After(now) {
		return "", record.nextAttempt.Sub(now), false
	}
	// Comparing digests keeps the comparison constant-time whatever the
	// lengths of the two values are.
	given, want := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(a.password))
	if a.password == "" || subtle.ConstantTimeCompare(given[:], want[:]) != 1 {
		return "", a.recordFailure(client, now), false
	}
	delete(a.failures, client)

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand does not fail on a working system; without it no
		// session can be trusted.
		panic(fmt.Sprintf("webapi: read random session id: %v", err))
	}
	session = base64.RawURLEncoding.EncodeToString(raw)
	if a.sessions == nil {
		a.sessions = make(map[string]time.Time)
	}
	a.pruneSessions(now)
	a.sessions[session] = now.Add(sessionLifetime)
	return session, 0, true
}

// recordFailure counts a failed sign-in and returns the wait it earned.
func (a *auth) recordFailure(client string, now time.Time) time.Duration {
	if a.failures == nil {
		a.failures = make(map[string]*failure)
	}
	record := a.failures[client]
	if record == nil {
		if len(a.failures) >= maxThrottled {
			a.dropOldestFailure()
		}
		record = &failure{}
		a.failures[client] = record
	}
	record.count++
	record.last = now
	if record.count < freeAttempts {
		return 0
	}
	record.nextAttempt = now.Add(loginDelay)
	return loginDelay
}

func (a *auth) dropOldestFailure() {
	var oldest string
	for client, record := range a.failures {
		if oldest == "" || record.last.Before(a.failures[oldest].last) {
			oldest = client
		}
	}
	delete(a.failures, oldest)
}

func (a *auth) pruneSessions(now time.Time) {
	for session, expires := range a.sessions {
		if !expires.After(now) {
			delete(a.sessions, session)
		}
	}
	for len(a.sessions) >= maxSessions {
		var oldest string
		for session, expires := range a.sessions {
			if oldest == "" || expires.Before(a.sessions[oldest]) {
				oldest = session
			}
		}
		delete(a.sessions, oldest)
	}
}

func (a *auth) logout(r *http.Request) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, cookie.Value)
}

// sessionCookieFor builds the cookie that carries a session. An empty value
// clears it. Secure is set whenever the browser reached the daemon over
// HTTPS, directly or through a proxy that says so.
func sessionCookieFor(r *http.Request, value string) *http.Cookie {
	cookie := &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https"),
		MaxAge:   int(sessionLifetime / time.Second),
	}
	if value == "" {
		cookie.MaxAge = -1
	}
	return cookie
}

// clientKey identifies who is signing in, for the throttle: the address the
// connection came from. Behind a proxy that is the proxy, which throttles
// everyone together; that errs on the safe side for a single shared password.
// An IPv6 peer is keyed by its /64, the block one site is routinely given, or
// it could take five free attempts from each of its addresses. IPv4, and IPv4
// mapped into IPv6, stays the full address.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr.WithZone(""), 64).Masked().String()
}
