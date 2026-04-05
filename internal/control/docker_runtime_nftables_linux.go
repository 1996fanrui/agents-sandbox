//go:build linux

package control

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
)

// realNftablesConnector manipulates nftables rules via netlink syscalls.
type realNftablesConnector struct {
	logger *slog.Logger
}

// nftIfname pads a network interface name to IFNAMSIZ (16 bytes) with a null
// terminator, matching the kernel representation used in nftables meta matches.
func nftIfname(name string) []byte {
	b := make([]byte, 16)
	copy(b, name+"\x00")
	return b
}

// rtnLocal is the kernel RTN_LOCAL routing type value (see linux/rtnetlink.h).
const rtnLocal = 2

// buildHostIsolationExprs constructs the nftables expression list that drops
// all traffic entering via `bridge` from `subnet` destined for any host-local
// address. This is equivalent to:
//
//	iptables -I DOCKER-USER -i <bridge> -s <subnet> -m addrtype --dst-type LOCAL -j DROP
func buildHostIsolationExprs(bridge string, subnet *net.IPNet) []expr.Any {
	// Mask the subnet IP to its canonical network form (e.g. 172.18.0.0 for /16).
	networkIP := subnet.IP.Mask(subnet.Mask)

	return []expr.Any{
		// Match input interface name.
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{
			Op:       expr.CmpOpEq,
			Register: 1,
			Data:     nftIfname(bridge),
		},
		// Load IPv4 source address (offset 12, length 4 in the IP header).
		&expr.Payload{
			DestRegister: 1,
			Base:         expr.PayloadBaseNetworkHeader,
			Offset:       12,
			Len:          4,
		},
		// Apply subnet mask via bitwise AND.
		&expr.Bitwise{
			SourceRegister: 1,
			DestRegister:   1,
			Len:            4,
			Mask:           []byte(subnet.Mask),
			Xor:            []byte{0, 0, 0, 0},
		},
		// Compare masked source against the network address.
		&expr.Cmp{
			Op:       expr.CmpOpEq,
			Register: 1,
			Data:     []byte(networkIP.To4()),
		},
		// Check if destination address type is LOCAL (fib lookup).
		&expr.Fib{
			Register:       1,
			FlagDADDR:      true,
			ResultADDRTYPE: true,
		},
		&expr.Cmp{
			Op:       expr.CmpOpEq,
			Register: 1,
			Data:     binaryutil.NativeEndian.PutUint32(rtnLocal),
		},
		// DROP verdict.
		&expr.Verdict{Kind: expr.VerdictDrop},
	}
}

// findDockerUserChain locates the DOCKER-USER chain in the IPv4 filter table.
func findDockerUserChain(conn *nftables.Conn) (*nftables.Chain, *nftables.Table, error) {
	chains, err := conn.ListChainsOfTableFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return nil, nil, fmt.Errorf("list nftables chains: %w", err)
	}
	for _, chain := range chains {
		if chain.Name == "DOCKER-USER" && chain.Table != nil && chain.Table.Name == "filter" {
			return chain, chain.Table, nil
		}
	}
	return nil, nil, fmt.Errorf("DOCKER-USER chain not found in filter table")
}

// matchesHostIsolationRule checks whether an existing nftables rule matches
// the host isolation expressions for the given bridge and subnet.
func matchesHostIsolationRule(rule *nftables.Rule, bridge string, subnet *net.IPNet) bool {
	expected := buildHostIsolationExprs(bridge, subnet)
	if len(rule.Exprs) != len(expected) {
		return false
	}
	// Compare interface name bytes (expression index 1) and subnet bytes (expression index 4).
	// These two fields uniquely identify an isolation rule for a specific bridge/subnet pair.
	if len(rule.Exprs) < 5 {
		return false
	}
	cmpIface, ok := rule.Exprs[1].(*expr.Cmp)
	if !ok {
		return false
	}
	expectedIface := nftIfname(bridge)
	if !bytes.Equal(cmpIface.Data, expectedIface) {
		return false
	}
	cmpSubnet, ok := rule.Exprs[4].(*expr.Cmp)
	if !ok {
		return false
	}
	networkIP := subnet.IP.Mask(subnet.Mask)
	if !bytes.Equal(cmpSubnet.Data, []byte(networkIP.To4())) {
		return false
	}
	return true
}

func (r *realNftablesConnector) applyHostIsolation(bridge string, subnet *net.IPNet) error {
	conn, err := nftables.New()
	if err != nil {
		return fmt.Errorf("open nftables connection: %w", err)
	}
	chain, table, err := findDockerUserChain(conn)
	if err != nil {
		return err
	}

	// Check if the rule already exists (idempotency).
	rules, err := conn.GetRules(table, chain)
	if err != nil {
		return fmt.Errorf("get nftables rules: %w", err)
	}
	for _, rule := range rules {
		if matchesHostIsolationRule(rule, bridge, subnet) {
			r.logger.Info("nftables host isolation rule already exists",
				slog.String("bridge", bridge),
				slog.String("subnet", subnet.String()),
			)
			return nil
		}
	}

	// Insert rule at the top of the chain.
	conn.InsertRule(&nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: buildHostIsolationExprs(bridge, subnet),
	})
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flush nftables rule for bridge %s: %w", bridge, err)
	}
	r.logger.Info("applied nftables host isolation rule",
		slog.String("bridge", bridge),
		slog.String("subnet", subnet.String()),
	)
	return nil
}

func (r *realNftablesConnector) removeHostIsolation(bridge string, subnet *net.IPNet) {
	conn, err := nftables.New()
	if err != nil {
		r.logger.Warn("failed to open nftables connection for cleanup",
			slog.String("bridge", bridge),
			slog.Any("error", err),
		)
		return
	}
	chain, table, err := findDockerUserChain(conn)
	if err != nil {
		r.logger.Warn("failed to find DOCKER-USER chain for cleanup",
			slog.String("bridge", bridge),
			slog.Any("error", err),
		)
		return
	}

	rules, err := conn.GetRules(table, chain)
	if err != nil {
		r.logger.Warn("failed to get nftables rules for cleanup",
			slog.String("bridge", bridge),
			slog.Any("error", err),
		)
		return
	}

	for _, rule := range rules {
		if matchesHostIsolationRule(rule, bridge, subnet) {
			if err := conn.DelRule(rule); err != nil {
				r.logger.Warn("failed to delete nftables host isolation rule",
					slog.String("bridge", bridge),
					slog.Any("error", err),
				)
				return
			}
			if err := conn.Flush(); err != nil {
				r.logger.Warn("failed to flush nftables rule deletion",
					slog.String("bridge", bridge),
					slog.Any("error", err),
				)
				return
			}
			r.logger.Info("removed nftables host isolation rule",
				slog.String("bridge", bridge),
				slog.String("subnet", subnet.String()),
			)
			return
		}
	}
	r.logger.Info("no matching nftables host isolation rule to remove",
		slog.String("bridge", bridge),
	)
}

// newNftablesConnector creates a real nftables connector for Linux.
func newNftablesConnector(logger *slog.Logger) nftablesConnector {
	return &realNftablesConnector{logger: logger}
}
