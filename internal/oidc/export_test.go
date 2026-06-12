package oidc

import "context"

// ExchangeTokenForTest exposes ID token verification to external tests,
// performing discovery first so the JWKS endpoint is known.
func (p *Provider) ExchangeTokenForTest(ctx context.Context, raw, nonce string) (*Claims, error) {
	if _, err := p.discover(ctx); err != nil {
		return nil, err
	}
	return p.verifyIDToken(ctx, raw, nonce)
}
