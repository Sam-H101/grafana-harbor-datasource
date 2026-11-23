package plugin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"

	"github.com/Sam-H101/grafana-harbor-datasource/pkg/harbor"
	"github.com/Sam-H101/grafana-harbor-datasource/pkg/models"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// problem is an error translated for a user, with the status and error
// source Grafana should record.
type problem struct {
	status     backend.Status
	message    string
	downstream bool
}

// denied is a 403 for a known area: it names the permission that is missing.
type denied struct {
	what       string
	permission string
}

func (e *denied) Error() string { return "harbor: forbidden: " + e.what }
func (e *denied) Unwrap() error { return harbor.ErrForbidden }

// needs turns a refusal into a denied error naming the permission to grant.
func needs(err error, what, permission string) error {
	var already *denied
	if errors.As(err, &already) {
		// A more specific place has named the permission.
		return err
	}
	if errors.Is(err, harbor.ErrForbidden) {
		return &denied{what: what, permission: permission}
	}
	return err
}

// explain turns any error from settings, queries or the Harbor client into a
// message for the panel or the health check. Messages never contain the
// password (the URL may not carry credentials) or raw stack traces.
func (d *Datasource) explain(err error) problem {
	var (
		apiErr  *harbor.APIError
		deny    *denied
		netErr  net.Error
		dnsErr  *net.DNSError
		opErr   *net.OpError
		urlErr  *url.Error
		unkCA   x509.UnknownAuthorityError
		hostErr x509.HostnameError
		certErr x509.CertificateInvalidError
		recErr  tls.RecordHeaderError
		verErr  *tls.CertificateVerificationError
	)
	hasCredentials := d.client != nil && d.client.HasCredentials()
	switch {
	case errors.Is(err, models.ErrInvalidSettings):
		return problem{backend.StatusBadRequest, "Configuration problem: " + strings.TrimPrefix(err.Error(), models.ErrInvalidSettings.Error()+": "), true}
	case errors.Is(err, models.ErrInvalidQuery):
		msg := strings.TrimPrefix(err.Error(), models.ErrInvalidQuery.Error()+": ")
		return problem{backend.StatusBadRequest, strings.ToUpper(msg[:1]) + msg[1:] + ".", true}
	case errors.Is(err, errNoSuchProject), errors.Is(err, errNoSuchPolicy):
		msg := err.Error()
		return problem{backend.StatusBadRequest, strings.ToUpper(msg[:1]) + msg[1:] + ".", true}
	case errors.Is(err, harbor.ErrUnauthorized):
		if hasCredentials {
			return problem{backend.StatusUnauthorized, "Harbor rejected the user name or password. Check both in the data source settings. For a robot account, the user name is the full name, such as robot$grafana, and the password is its secret.", true}
		}
		return problem{backend.StatusUnauthorized, "Harbor requires a login for this. Enter a robot account or user in the data source settings.", true}
	case errors.As(err, &deny):
		return problem{backend.StatusForbidden, fmt.Sprintf("The Harbor account is not allowed to read %s. Give the robot account the permission %s, or use an account with the system administrator role.", deny.what, deny.permission), true}
	case errors.Is(err, harbor.ErrForbidden):
		return problem{backend.StatusForbidden, "The Harbor account is not allowed to read this. Check its permissions, and that the project exists.", true}
	case errors.As(err, &apiErr):
		return problem{backend.StatusBadRequest, "Harbor: " + strings.TrimSuffix(apiErr.Message, ".") + ".", true}
	case errors.Is(err, harbor.ErrNotHarbor):
		return problem{backend.StatusBadGateway, "The server at " + d.url() + " did not answer like Harbor. Check the URL: use the address you open Harbor at in a browser, without /harbor or /api.", true}
	case errors.Is(err, harbor.ErrResponseTooLarge):
		return problem{backend.StatusBadRequest, "The answer from Harbor is too large. Narrow the query or lower the limit.", true}
	case errors.Is(err, context.DeadlineExceeded):
		secs := 0
		if d.settings != nil {
			secs = int(d.settings.Timeout.Seconds())
		}
		return problem{backend.StatusTimeout, fmt.Sprintf("Harbor did not answer within %d seconds. Narrow the query or lower the limit, or raise the request timeout in the data source settings.", secs), true}
	case errors.Is(err, context.Canceled):
		return problem{backend.StatusTimeout, "The query was cancelled.", true}
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET):
		return problem{backend.StatusBadGateway, "Harbor at " + d.url() + " closed the connection before answering. It may be restarting.", true}
	case errors.As(err, &unkCA), errors.As(err, &hostErr), errors.As(err, &certErr), errors.As(err, &verErr), errors.As(err, &recErr):
		return problem{backend.StatusBadGateway, "TLS error connecting to Harbor: " + innermost(err) + ". Check the URL scheme and the TLS settings.", true}
	case strings.Contains(err.Error(), "malformed HTTP response"):
		// Typically TLS alert bytes: an http:// URL pointing at an HTTPS port.
		return problem{backend.StatusBadGateway, "The server at " + d.url() + " did not answer with HTTP. If Harbor is served over HTTPS, change the URL to https://.", true}
	case strings.Contains(err.Error(), "server gave HTTP response to HTTPS client"):
		return problem{backend.StatusBadGateway, "Harbor at " + d.url() + " answered with plain HTTP. Change the URL to http://, or put Harbor behind HTTPS.", true}
	case errors.As(err, &dnsErr):
		return problem{backend.StatusBadGateway, "Cannot reach Harbor at " + d.url() + ": host not found." + proxyHint, true}
	case errors.As(err, &opErr), errors.As(err, &netErr), errors.As(err, &urlErr):
		return problem{backend.StatusBadGateway, "Cannot reach Harbor at " + d.url() + ": " + innermost(err) + "." + proxyHint, true}
	default:
		return problem{backend.StatusInternal, err.Error(), false}
	}
}

// proxyHint reminds users of a hosted Grafana that a private Harbor needs a
// route to it.
const proxyHint = " If Harbor is on a private network and Grafana is hosted elsewhere (for example Grafana Cloud), turn on Private data source connect in the data source settings."

// logSafe shortens a user-facing message for logs.
func logSafe(msg string) string {
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

func (d *Datasource) url() string {
	if d.settings == nil {
		return "the configured URL"
	}
	return d.settings.URL
}

// innermost strips url.Error's "Get \"...\":" prefix and similar wrapping.
func innermost(err error) string {
	for {
		u := errors.Unwrap(err)
		if u == nil {
			return err.Error()
		}
		err = u
	}
}

func (p problem) response() backend.DataResponse {
	src := backend.ErrorSourcePlugin
	if p.downstream {
		src = backend.ErrorSourceDownstream
	}
	return backend.ErrDataResponseWithSource(p.status, src, p.message)
}
