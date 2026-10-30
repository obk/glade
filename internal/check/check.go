// Package check runs one check: an HTTP(S) request or a TCP connection.
package check

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/obk/glade/internal/config"
)

// The states a check can be in, from best to worst.
const (
	Up       = "up"
	Degraded = "degraded" // answering, but slow or with a certificate about to expire
	Down     = "down"
)

// CertWarning is how close to its expiry a certificate makes a check
// degraded.
var CertWarning = 14 * 24 * time.Hour

type Result struct {
	Time  time.Time     `json:"time"`
	State string        `json:"state"`
	Took  time.Duration `json:"took"`
	Note  string        `json:"note,omitempty"` // why it isn't up, fit for the public page

	// Detail is the error in full, for glade's log only: it can hold the
	// check's URL, with any token in it, and internal names and addresses.
	Detail string `json:"-"`
}

var userAgent = "glade (+https://github.com/obk/glade)"

// Run checks once. A check that fails is tried once more after retryAfter,
// so that one lost packet doesn't count as an outage.
func Run(ch config.Check, client *http.Client, retryAfter time.Duration) Result {
	r := try(ch, client)
	if r.State == Down {
		time.Sleep(retryAfter)
		r = try(ch, client)
	}
	return r
}

func try(ch config.Check, client *http.Client) Result {
	start := time.Now()
	var r Result
	if ch.TCP != "" {
		r = tcp(ch)
	} else {
		r = httpGet(ch, client)
	}
	r.Time, r.Took = start.UTC(), time.Since(start)
	if r.State == Up && ch.Slow > 0 && r.Took > ch.Slow {
		r.State, r.Note = Degraded, fmt.Sprintf("slow: %s", r.Took.Round(time.Millisecond))
	}
	return r
}

func tcp(ch config.Check) Result {
	conn, err := net.DialTimeout("tcp", ch.TCP, ch.Timeout)
	if err != nil {
		return failed(err, ch.Timeout)
	}
	conn.Close()
	return Result{State: Up}
}

func httpGet(ch config.Check, client *http.Client) Result {
	ctx, cancel := context.WithTimeout(context.Background(), ch.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ch.URL, nil)
	if err != nil {
		return failed(err, ch.Timeout)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return failed(err, ch.Timeout)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The standard text, not the server's own, which could say anything.
		return Result{State: Down, Note: fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))}
	}
	if ch.Contains != "" {
		// A page bigger than a megabyte is read only that far.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return failed(err, ch.Timeout)
		}
		if !bytes.Contains(body, []byte(ch.Contains)) {
			return Result{State: Down, Note: "the page doesn't contain the expected text"}
		}
	}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		if left := time.Until(resp.TLS.PeerCertificates[0].NotAfter); left < CertWarning {
			return Result{State: Degraded, Note: fmt.Sprintf("the certificate expires in %s", days(left))}
		}
	}
	return Result{State: Up}
}

// failed is a check that got no answer: a short reason for the page, and
// the whole error for the log.
func failed(err error, timeout time.Duration) Result {
	return Result{State: Down, Note: reason(err, timeout), Detail: err.Error()}
}

// reason says why a check failed without the URL, host names or
// addresses that Go's errors include.
func reason(err error, timeout time.Duration) string {
	var (
		dns      *net.DNSError
		invalid  x509.CertificateInvalidError
		hostname x509.HostnameError
		unknown  x509.UnknownAuthorityError
		record   tls.RecordHeaderError
	)
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return fmt.Sprintf("no answer within %s", timeout)
	case errors.As(err, &dns):
		if dns.IsNotFound {
			return "the name doesn't exist in DNS"
		}
		return "the DNS lookup failed"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "the connection was closed"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "the host is unreachable"
	case errors.As(err, &invalid):
		if invalid.Reason == x509.Expired {
			return "the certificate has expired"
		}
		return "the certificate isn't valid"
	case errors.As(err, &hostname):
		return "the certificate is for another name"
	case errors.As(err, &unknown):
		return "the certificate isn't from a trusted authority"
	case errors.As(err, &record):
		return "the server didn't answer with TLS"
	}
	return "no answer"
}

func days(d time.Duration) string {
	n := int(d.Hours() / 24)
	if n == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}
