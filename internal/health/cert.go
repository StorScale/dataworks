package health

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"
	"time"
)

// CertExpiry is when the certificate the platform's address serves ends:
// zero, with no error, when the address isn't HTTPS.
func CertExpiry(ctx context.Context, platformURL string) (time.Time, error) {
	u, err := url.Parse(platformURL)
	if err != nil || u.Scheme != "https" {
		return time.Time{}, err
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	d := tls.Dialer{Config: &tls.Config{ServerName: u.Hostname(), InsecureSkipVerify: true}} // only its dates are read
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return time.Time{}, nil
	}
	return certs[0].NotAfter, nil
}
