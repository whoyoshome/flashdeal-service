// Command token mints HS256 JWTs for local demos, load tests and curl.
// Production tokens come from an IdP; this tool shares the API's secret.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/platform/auth"
)

func main() {
	subject := flag.String("sub", "alice", "JWT subject (user id)")
	role := flag.String("role", "user", "role claim: user or admin")
	ttl := flag.Duration("ttl", time.Hour, "token lifetime")
	secret := flag.String("secret", "", "HMAC secret (required; same as JWT_SECRET)")
	issuer := flag.String("issuer", "flashdeal", "iss claim")
	flag.Parse()

	if *secret == "" {
		fmt.Fprintln(os.Stderr, "token: -secret is required (use the same value as JWT_SECRET)")
		os.Exit(2)
	}
	svc, err := auth.New(auth.Config{Secret: *secret, Issuer: *issuer})
	if err != nil {
		fmt.Fprintf(os.Stderr, "token: %v\n", err)
		os.Exit(2)
	}
	tok, err := svc.Issue(*subject, auth.Role(*role), *ttl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "token: %v\n", err)
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(os.Stdout, tok)
}
