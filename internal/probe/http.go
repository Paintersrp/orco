package probe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/Paintersrp/orco/internal/stack"
)

type httpProber struct {
	client *http.Client
	url    string
	expect []int
}

func newHTTPProber(spec *stack.HTTPProbe) Prober {
	client := &http.Client{}
	return &httpProber{
		client: client,
		url:    spec.URL,
		expect: append([]int(nil), spec.ExpectStatus...),
	}
}

func (p *httpProber) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if len(p.expect) > 0 {
		if !slices.Contains(p.expect, resp.StatusCode) {
			return fmt.Errorf("status=%d", resp.StatusCode)
		}
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("status=%d", resp.StatusCode)
	}
	return nil
}
