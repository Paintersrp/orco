package probe

import (
	"context"
	"fmt"
	"net"

	"github.com/Paintersrp/orco/internal/stack"
)

type tcpProber struct {
	address string
	dialer  func(ctx context.Context, network, address string) (net.Conn, error)
}

func newTCPProber(spec *stack.TCPProbe) Prober {
	return &tcpProber{
		address: spec.Address,
		dialer:  (&net.Dialer{}).DialContext,
	}
}

func (p *tcpProber) Probe(ctx context.Context) error {
	conn, err := p.dialer(ctx, "tcp", p.address)
	if err != nil {
		return fmt.Errorf("dial %s: %w", p.address, err)
	}
	return conn.Close()
}
