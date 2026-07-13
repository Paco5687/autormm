package agent

import "crypto/tls"

// insecureTLS returns a client config that skips certificate verification, for
// homelab servers using self-signed certs. Enabled only via --insecure.
func insecureTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true}
}
