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

// ipsDstNAT is the IPS_DST_NAT bit from linux/netfilter/nf_conntrack_common.h.
// When set in conntrack status, it indicates the packet's destination was
// rewritten by DNAT (e.g. Docker port mapping via -p host:container).
const ipsDstNAT = 1 << 5 // 32

// buildHostIsolationExprs constructs the nftables expression list that drops
// all traffic entering via `bridge` from `subnet` destined for any host-local
// address. This is equivalent to:
//
//	iptables -I DOCKER-USER -i <bridge> -s <subnet> -m addrtype --dst-type LOCAL -j DROP
func buildHostIsolationExprs(bridge string, subnet *net.IPNet) []expr.Any {
	return buildBridgeSubnetMatchExprs(bridge, subnet, []expr.Any{
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
	}...)
}

// buildDNATIsolationExprs constructs the nftables expression list that drops
// all DNAT'd traffic entering via `bridge` from `subnet`. Docker port mappings
// (-p host:container) rewrite the destination in PREROUTING/nat before packets
// reach DOCKER-USER in filter/FORWARD, so the dst-type LOCAL check in
// buildHostIsolationExprs never matches them. This rule catches that case by
// matching conntrack status IPS_DST_NAT. Equivalent to:
//
//	iptables -I DOCKER-USER -i <bridge> -s <subnet> -m conntrack --ctstate DNAT -j DROP
func buildDNATIsolationExprs(bridge string, subnet *net.IPNet) []expr.Any {
	return buildBridgeSubnetMatchExprs(bridge, subnet, []expr.Any{
		// Load conntrack status into register 1.
		&expr.Ct{
			Register: 1,
			Key:      expr.CtKeySTATUS,
		},
		// Bitwise AND with IPS_DST_NAT to isolate the DNAT bit.
		&expr.Bitwise{
			SourceRegister: 1,
			DestRegister:   1,
			Len:            4,
			Mask:           binaryutil.NativeEndian.PutUint32(ipsDstNAT),
			Xor:            []byte{0, 0, 0, 0},
		},
		// If the DNAT bit is set (non-zero), this packet was DNAT'd.
		&expr.Cmp{
			Op:       expr.CmpOpNeq,
			Register: 1,
			Data:     binaryutil.NativeEndian.PutUint32(0),
		},
		// DROP verdict.
		&expr.Verdict{Kind: expr.VerdictDrop},
	}...)
}

// buildHostInputIsolationExprs constructs the nftables expression list that
// drops packets from the sandbox subnet entering the host INPUT path via the
// sandbox bridge. This blocks direct access to the bridge gateway address and
// Docker userland-proxy listeners bound on host addresses.
func buildHostInputIsolationExprs(bridge string, subnet *net.IPNet) []expr.Any {
	return buildBridgeSubnetMatchExprs(bridge, subnet, &expr.Verdict{Kind: expr.VerdictDrop})
}

// buildBridgeSubnetMatchExprs constructs the common prefix expressions that
// match traffic entering via `bridge` from `subnet`, followed by the provided
// tail expressions. Both host isolation rules share this prefix.
func buildBridgeSubnetMatchExprs(bridge string, subnet *net.IPNet, tail ...expr.Any) []expr.Any {
	networkIP := subnet.IP.Mask(subnet.Mask)

	prefix := []expr.Any{
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
	}
	return append(prefix, tail...)
}

func findFilterChain(conn *nftables.Conn, name string) (*nftables.Chain, *nftables.Table, error) {
	chains, err := conn.ListChainsOfTableFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return nil, nil, fmt.Errorf("list nftables chains: %w", err)
	}
	for _, chain := range chains {
		if chain.Name == name && chain.Table != nil && chain.Table.Name == "filter" {
			return chain, chain.Table, nil
		}
	}
	return nil, nil, fmt.Errorf("%s chain not found in filter table", name)
}

// findDockerUserChain locates the DOCKER-USER chain in the IPv4 filter table.
func findDockerUserChain(conn *nftables.Conn) (*nftables.Chain, *nftables.Table, error) {
	return findFilterChain(conn, "DOCKER-USER")
}

func findInputChain(conn *nftables.Conn) (*nftables.Chain, *nftables.Table, error) {
	return findFilterChain(conn, "INPUT")
}

// agboxInputChainName is the name of the nftables chain we create when the
// standard INPUT chain is absent (native nftables mode, e.g. Ubuntu 24).
const agboxInputChainName = "AGBOX-INPUT"

func expectedDockerUserIsolationRules(bridge string, subnet *net.IPNet) [][]expr.Any {
	return [][]expr.Any{
		buildHostIsolationExprs(bridge, subnet),
		buildDNATIsolationExprs(bridge, subnet),
	}
}

func expectedInputIsolationRules(bridge string, subnet *net.IPNet) [][]expr.Any {
	return [][]expr.Any{
		buildHostInputIsolationExprs(bridge, subnet),
	}
}

// expectedHostIsolationRules returns all nftables rules that together enforce
// host isolation for one sandbox bridge.
func expectedHostIsolationRules(bridge string, subnet *net.IPNet) [][]expr.Any {
	return append(
		expectedDockerUserIsolationRules(bridge, subnet),
		expectedInputIsolationRules(bridge, subnet)...,
	)
}

// matchesHostIsolationRule checks whether an existing nftables rule matches
// any host isolation rule (host-local or DNAT) for the given bridge and subnet.
func matchesHostIsolationRule(rule *nftables.Rule, bridge string, subnet *net.IPNet) bool {
	for _, expected := range expectedHostIsolationRules(bridge, subnet) {
		if matchesIsolationRule(rule, expected) {
			return true
		}
	}
	return false
}

// matchesIsolationRule checks whether a rule matches the expression shape used
// by one expected isolation rule for a specific bridge/subnet pair.
func matchesIsolationRule(rule *nftables.Rule, expected []expr.Any) bool {
	if len(rule.Exprs) != len(expected) {
		return false
	}
	if len(rule.Exprs) < 5 {
		return false
	}
	cmpIface, ok := rule.Exprs[1].(*expr.Cmp)
	if !ok {
		return false
	}
	expectedIface, ok := expected[1].(*expr.Cmp)
	if !ok {
		return false
	}
	if !bytes.Equal(cmpIface.Data, expectedIface.Data) {
		return false
	}
	cmpSubnet, ok := rule.Exprs[4].(*expr.Cmp)
	if !ok {
		return false
	}
	expectedSubnet, ok := expected[4].(*expr.Cmp)
	if !ok {
		return false
	}
	if !bytes.Equal(cmpSubnet.Data, expectedSubnet.Data) {
		return false
	}
	return matchesIsolationTail(rule.Exprs[5:], expected[5:])
}

func matchesIsolationTail(actual []expr.Any, expected []expr.Any) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i := range expected {
		switch expectedExpr := expected[i].(type) {
		case *expr.Fib:
			actualExpr, ok := actual[i].(*expr.Fib)
			if !ok {
				return false
			}
			if actualExpr.Register != expectedExpr.Register ||
				actualExpr.FlagDADDR != expectedExpr.FlagDADDR ||
				actualExpr.ResultADDRTYPE != expectedExpr.ResultADDRTYPE {
				return false
			}
		case *expr.Ct:
			actualExpr, ok := actual[i].(*expr.Ct)
			if !ok {
				return false
			}
			if actualExpr.Register != expectedExpr.Register ||
				actualExpr.Key != expectedExpr.Key ||
				actualExpr.SourceRegister != expectedExpr.SourceRegister ||
				actualExpr.Direction != expectedExpr.Direction {
				return false
			}
		case *expr.Bitwise:
			actualExpr, ok := actual[i].(*expr.Bitwise)
			if !ok {
				return false
			}
			if actualExpr.SourceRegister != expectedExpr.SourceRegister ||
				actualExpr.DestRegister != expectedExpr.DestRegister ||
				actualExpr.Len != expectedExpr.Len ||
				!bytes.Equal(actualExpr.Mask, expectedExpr.Mask) ||
				!bytes.Equal(actualExpr.Xor, expectedExpr.Xor) {
				return false
			}
		case *expr.Cmp:
			actualExpr, ok := actual[i].(*expr.Cmp)
			if !ok {
				return false
			}
			if actualExpr.Op != expectedExpr.Op ||
				actualExpr.Register != expectedExpr.Register ||
				!bytes.Equal(actualExpr.Data, expectedExpr.Data) {
				return false
			}
		case *expr.Verdict:
			actualExpr, ok := actual[i].(*expr.Verdict)
			if !ok {
				return false
			}
			if actualExpr.Kind != expectedExpr.Kind {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (r *realNftablesConnector) applyHostIsolation(bridge string, subnet *net.IPNet) error {
	conn, err := nftables.New()
	if err != nil {
		return fmt.Errorf("open nftables connection: %w", err)
	}
	dockerUserChain, dockerUserTable, err := findDockerUserChain(conn)
	if err != nil {
		return err
	}

	inserted := 0

	n, err := insertMissingIsolationRules(conn, dockerUserTable, dockerUserChain, expectedDockerUserIsolationRules(bridge, subnet))
	if err != nil {
		return err
	}
	inserted += n

	// Add INPUT isolation rules. On native nftables (Ubuntu 24+) the standard
	// INPUT chain is absent; in that case we create our own AGBOX-INPUT chain
	// with an input hook so docker-proxy traffic is blocked just as reliably.
	inputN, err := r.applyInputIsolation(conn, dockerUserTable, bridge, subnet)
	if err != nil {
		return err
	}
	inserted += inputN

	if inserted == 0 {
		r.logger.Info("nftables host isolation rules already exist",
			slog.String("bridge", bridge),
			slog.String("subnet", subnet.String()),
		)
		return nil
	}

	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flush nftables rules for bridge %s: %w", bridge, err)
	}
	r.logger.Info("applied nftables host isolation rules",
		slog.String("bridge", bridge),
		slog.String("subnet", subnet.String()),
		slog.Int("inserted_rules", inserted),
	)
	return nil
}

// applyInputIsolation adds INPUT-path isolation rules for bridge/subnet.
// It uses the existing INPUT chain when present; otherwise it finds or creates
// the AGBOX-INPUT chain (hooked at input priority 0) inside the same filter
// table used by Docker, so docker-proxy traffic is always blocked.
func (r *realNftablesConnector) applyInputIsolation(
	conn *nftables.Conn,
	filterTable *nftables.Table,
	bridge string,
	subnet *net.IPNet,
) (int, error) {
	inputChain, inputTable, err := findInputChain(conn)
	if err == nil {
		return insertMissingIsolationRules(conn, inputTable, inputChain, expectedInputIsolationRules(bridge, subnet))
	}

	// Standard INPUT chain absent (native nftables mode). Find or create AGBOX-INPUT.
	agboxChain, agboxTable, err := findFilterChain(conn, agboxInputChainName)
	if err == nil {
		return insertMissingIsolationRules(conn, agboxTable, agboxChain, expectedInputIsolationRules(bridge, subnet))
	}

	// AGBOX-INPUT doesn't exist yet. Create it and add rules in the same
	// transaction; no GetRules needed since the chain is brand new.
	newChain := conn.AddChain(&nftables.Chain{
		Name:     agboxInputChainName,
		Table:    filterTable,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookInput,
		Priority: nftables.ChainPriorityFilter,
	})
	expected := expectedInputIsolationRules(bridge, subnet)
	for _, exprs := range expected {
		conn.InsertRule(&nftables.Rule{
			Table: filterTable,
			Chain: newChain,
			Exprs: exprs,
		})
	}
	return len(expected), nil
}

func insertMissingIsolationRules(
	conn *nftables.Conn,
	table *nftables.Table,
	chain *nftables.Chain,
	expectedRules [][]expr.Any,
) (int, error) {
	rules, err := conn.GetRules(table, chain)
	if err != nil {
		return 0, fmt.Errorf("get nftables rules from %s: %w", chain.Name, err)
	}

	inserted := 0
	for _, expected := range expectedRules {
		exists := false
		for _, rule := range rules {
			if matchesIsolationRule(rule, expected) {
				exists = true
				break
			}
		}
		if exists {
			continue
		}

		conn.InsertRule(&nftables.Rule{
			Table: table,
			Chain: chain,
			Exprs: expected,
		})
		inserted++
	}
	return inserted, nil
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
	removed := 0
	dockerUserChain, dockerUserTable, err := findDockerUserChain(conn)
	if err == nil {
		n, err := removeIsolationRulesFromChain(conn, dockerUserTable, dockerUserChain, expectedDockerUserIsolationRules(bridge, subnet))
		if err != nil {
			r.logger.Warn("failed to delete nftables host isolation rule",
				slog.String("bridge", bridge),
				slog.Any("error", err),
			)
			return
		}
		removed += n
	} else {
		r.logger.Warn("failed to find DOCKER-USER chain for cleanup",
			slog.String("bridge", bridge),
			slog.Any("error", err),
		)
	}

	// Try both INPUT and AGBOX-INPUT chains; rules live in whichever existed
	// when the sandbox was created.
	for _, chainName := range []string{"INPUT", agboxInputChainName} {
		chain, table, lookupErr := findFilterChain(conn, chainName)
		if lookupErr != nil {
			continue
		}
		n, removeErr := removeIsolationRulesFromChain(conn, table, chain, expectedInputIsolationRules(bridge, subnet))
		if removeErr != nil {
			r.logger.Warn("failed to delete nftables host input isolation rule",
				slog.String("bridge", bridge),
				slog.String("chain", chainName),
				slog.Any("error", removeErr),
			)
			return
		}
		removed += n
	}

	if removed == 0 {
		r.logger.Info("no matching nftables host isolation rules to remove",
			slog.String("bridge", bridge),
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
	r.logger.Info("removed nftables host isolation rules",
		slog.String("bridge", bridge),
		slog.String("subnet", subnet.String()),
		slog.Int("removed_rules", removed),
	)
}

func removeIsolationRulesFromChain(
	conn *nftables.Conn,
	table *nftables.Table,
	chain *nftables.Chain,
	expectedRules [][]expr.Any,
) (int, error) {
	rules, err := conn.GetRules(table, chain)
	if err != nil {
		return 0, fmt.Errorf("get nftables rules from %s: %w", chain.Name, err)
	}

	removed := 0
	for _, rule := range rules {
		for _, expected := range expectedRules {
			if !matchesIsolationRule(rule, expected) {
				continue
			}
			if err := conn.DelRule(rule); err != nil {
				return removed, err
			}
			removed++
			break
		}
	}
	return removed, nil
}

// newNftablesConnector creates a real nftables connector for Linux.
func newNftablesConnector(logger *slog.Logger) nftablesConnector {
	return &realNftablesConnector{logger: logger}
}
