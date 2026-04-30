//go:build linux

package control

import (
	"bytes"
	"net"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
)

func TestBuildHostIsolationExprs(t *testing.T) {
	_, subnet, err := net.ParseCIDR("172.18.0.0/16")
	if err != nil {
		t.Fatalf("ParseCIDR failed: %v", err)
	}
	exprs := buildHostIsolationExprs("br-abc123def456", subnet)

	// Expected structure: common bridge/subnet prefix, ct state NEW, Fib, Cmp, Verdict.
	if len(exprs) != 11 {
		t.Fatalf("expected 11 expressions, got %d", len(exprs))
	}

	typeChecks := []string{"Meta", "Cmp", "Payload", "Bitwise", "Cmp", "Ct", "Bitwise", "Cmp", "Fib", "Cmp", "Verdict"}
	for i, name := range typeChecks {
		var ok bool
		switch name {
		case "Meta":
			_, ok = exprs[i].(*expr.Meta)
		case "Cmp":
			_, ok = exprs[i].(*expr.Cmp)
		case "Payload":
			_, ok = exprs[i].(*expr.Payload)
		case "Bitwise":
			_, ok = exprs[i].(*expr.Bitwise)
		case "Fib":
			_, ok = exprs[i].(*expr.Fib)
		case "Ct":
			_, ok = exprs[i].(*expr.Ct)
		case "Verdict":
			_, ok = exprs[i].(*expr.Verdict)
		}
		if !ok {
			t.Fatalf("exprs[%d]: expected %s, got %T", i, name, exprs[i])
		}
	}

	// Verify interface name match.
	cmpIface := exprs[1].(*expr.Cmp)
	expectedIfname := nftIfname("br-abc123def456")
	if !bytes.Equal(cmpIface.Data, expectedIfname) {
		t.Fatalf("interface name mismatch: got %v, want %v", cmpIface.Data, expectedIfname)
	}

	// Verify subnet mask.
	bitwise := exprs[3].(*expr.Bitwise)
	if !bytes.Equal(bitwise.Mask, []byte{255, 255, 0, 0}) {
		t.Fatalf("subnet mask mismatch: got %v, want [255 255 0 0]", bitwise.Mask)
	}

	// Verify network address comparison.
	cmpNet := exprs[4].(*expr.Cmp)
	if !bytes.Equal(cmpNet.Data, []byte{172, 18, 0, 0}) {
		t.Fatalf("network address mismatch: got %v, want [172 18 0 0]", cmpNet.Data)
	}

	assertConntrackNewMatch(t, exprs[5:8])

	// Verify fib RTN_LOCAL comparison uses native endian.
	cmpFib := exprs[9].(*expr.Cmp)
	expectedRTNLocal := binaryutil.NativeEndian.PutUint32(rtnLocal)
	if !bytes.Equal(cmpFib.Data, expectedRTNLocal) {
		t.Fatalf("RTN_LOCAL mismatch: got %v, want %v", cmpFib.Data, expectedRTNLocal)
	}

	// Verify DROP verdict.
	verdict := exprs[10].(*expr.Verdict)
	if verdict.Kind != expr.VerdictDrop {
		t.Fatalf("expected VerdictDrop, got %v", verdict.Kind)
	}
}

func TestBuildDNATIsolationExprs(t *testing.T) {
	_, subnet, err := net.ParseCIDR("172.18.0.0/16")
	if err != nil {
		t.Fatalf("ParseCIDR failed: %v", err)
	}
	exprs := buildDNATIsolationExprs("br-abc123def456", subnet)

	// Expected structure: common bridge/subnet prefix, ct state NEW, Ct status DNAT, Verdict.
	if len(exprs) != 12 {
		t.Fatalf("expected 12 expressions, got %d", len(exprs))
	}

	assertConntrackNewMatch(t, exprs[5:8])

	ct, ok := exprs[8].(*expr.Ct)
	if !ok {
		t.Fatalf("exprs[8]: expected Ct, got %T", exprs[8])
	}
	if ct.Key != expr.CtKeySTATUS || ct.Register != 1 {
		t.Fatalf("unexpected Ct expression: key=%v register=%d", ct.Key, ct.Register)
	}

	bitwise, ok := exprs[9].(*expr.Bitwise)
	if !ok {
		t.Fatalf("exprs[9]: expected Bitwise, got %T", exprs[9])
	}
	expectedDNATMask := binaryutil.NativeEndian.PutUint32(ipsDstNAT)
	if !bytes.Equal(bitwise.Mask, expectedDNATMask) {
		t.Fatalf("DNAT mask mismatch: got %v, want %v", bitwise.Mask, expectedDNATMask)
	}

	cmpDNAT, ok := exprs[10].(*expr.Cmp)
	if !ok {
		t.Fatalf("exprs[10]: expected Cmp, got %T", exprs[10])
	}
	if cmpDNAT.Op != expr.CmpOpNeq || !bytes.Equal(cmpDNAT.Data, binaryutil.NativeEndian.PutUint32(0)) {
		t.Fatalf("unexpected DNAT comparison: op=%v data=%v", cmpDNAT.Op, cmpDNAT.Data)
	}

	verdict, ok := exprs[11].(*expr.Verdict)
	if !ok {
		t.Fatalf("exprs[11]: expected Verdict, got %T", exprs[11])
	}
	if verdict.Kind != expr.VerdictDrop {
		t.Fatalf("expected VerdictDrop, got %v", verdict.Kind)
	}
}

func TestBuildHostInputIsolationExprs(t *testing.T) {
	_, subnet, err := net.ParseCIDR("172.18.0.0/16")
	if err != nil {
		t.Fatalf("ParseCIDR failed: %v", err)
	}
	exprs := buildHostInputIsolationExprs("br-abc123def456", subnet)

	if len(exprs) != 9 {
		t.Fatalf("expected 9 expressions, got %d", len(exprs))
	}

	cmpIface := exprs[1].(*expr.Cmp)
	expectedIfname := nftIfname("br-abc123def456")
	if !bytes.Equal(cmpIface.Data, expectedIfname) {
		t.Fatalf("interface name mismatch: got %v, want %v", cmpIface.Data, expectedIfname)
	}

	cmpNet := exprs[4].(*expr.Cmp)
	if !bytes.Equal(cmpNet.Data, []byte{172, 18, 0, 0}) {
		t.Fatalf("network address mismatch: got %v, want [172 18 0 0]", cmpNet.Data)
	}

	assertConntrackNewMatch(t, exprs[5:8])

	verdict, ok := exprs[8].(*expr.Verdict)
	if !ok {
		t.Fatalf("exprs[8]: expected Verdict, got %T", exprs[8])
	}
	if verdict.Kind != expr.VerdictDrop {
		t.Fatalf("expected VerdictDrop, got %v", verdict.Kind)
	}
}

func assertConntrackNewMatch(t *testing.T, exprs []expr.Any) {
	t.Helper()
	if len(exprs) != 3 {
		t.Fatalf("expected 3 conntrack NEW expressions, got %d", len(exprs))
	}
	ct, ok := exprs[0].(*expr.Ct)
	if !ok {
		t.Fatalf("conntrack expr[0]: expected Ct, got %T", exprs[0])
	}
	if ct.Key != expr.CtKeySTATE || ct.Register != 1 {
		t.Fatalf("unexpected conntrack state expression: key=%v register=%d", ct.Key, ct.Register)
	}
	bitwise, ok := exprs[1].(*expr.Bitwise)
	if !ok {
		t.Fatalf("conntrack expr[1]: expected Bitwise, got %T", exprs[1])
	}
	expectedNewMask := binaryutil.NativeEndian.PutUint32(expr.CtStateBitNEW)
	if bitwise.SourceRegister != 1 ||
		bitwise.DestRegister != 1 ||
		bitwise.Len != 4 ||
		!bytes.Equal(bitwise.Mask, expectedNewMask) ||
		!bytes.Equal(bitwise.Xor, []byte{0, 0, 0, 0}) {
		t.Fatalf("unexpected conntrack NEW bitwise expression: %#v", bitwise)
	}
	cmp, ok := exprs[2].(*expr.Cmp)
	if !ok {
		t.Fatalf("conntrack expr[2]: expected Cmp, got %T", exprs[2])
	}
	if cmp.Op != expr.CmpOpNeq ||
		cmp.Register != 1 ||
		!bytes.Equal(cmp.Data, binaryutil.NativeEndian.PutUint32(0)) {
		t.Fatalf("unexpected conntrack NEW comparison: %#v", cmp)
	}
}

func TestMatchesHostIsolationRule(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("172.18.0.0/16")
	bridge := "br-abc123def456"

	hostRule := &nftables.Rule{
		Exprs: buildHostIsolationExprs(bridge, subnet),
	}
	if !matchesHostIsolationRule(hostRule, bridge, subnet) {
		t.Fatal("expected host-local rule to be detected")
	}

	dnatRule := &nftables.Rule{
		Exprs: buildDNATIsolationExprs(bridge, subnet),
	}
	if !matchesHostIsolationRule(dnatRule, bridge, subnet) {
		t.Fatal("expected DNAT rule to be detected")
	}

	inputRule := &nftables.Rule{
		Exprs: buildHostInputIsolationExprs(bridge, subnet),
	}
	if !matchesHostIsolationRule(inputRule, bridge, subnet) {
		t.Fatal("expected INPUT rule to be detected")
	}

	if matchesHostIsolationRule(hostRule, "br-other", subnet) {
		t.Fatal("different bridge should not match")
	}

	_, otherSubnet, _ := net.ParseCIDR("10.0.0.0/24")
	if matchesHostIsolationRule(dnatRule, bridge, otherSubnet) {
		t.Fatal("different subnet should not match")
	}

	shortRule := &nftables.Rule{
		Exprs: []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}},
	}
	if matchesHostIsolationRule(shortRule, bridge, subnet) {
		t.Fatal("rule with wrong expression count should not match")
	}

	wrongTailRule := &nftables.Rule{
		Exprs: buildDNATIsolationExprs(bridge, subnet),
	}
	wrongTailRule.Exprs[5] = &expr.Fib{
		Register:       1,
		FlagDADDR:      true,
		ResultADDRTYPE: true,
	}
	if matchesHostIsolationRule(wrongTailRule, bridge, subnet) {
		t.Fatal("rule with wrong tail expression should not match")
	}
}

func TestHostIsolationCurrentRulesMatchConntrackNew(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("172.18.0.0/16")
	_, otherSubnet, _ := net.ParseCIDR("172.19.0.0/16")
	bridge := "br-abc123def456"

	for _, exprs := range expectedHostIsolationRules(bridge, subnet) {
		assertConntrackNewMatch(t, exprs[5:8])
		rule := &nftables.Rule{Exprs: exprs}
		if !matchesCurrentHostIsolationRule(rule, bridge, subnet) {
			t.Fatalf("current rule did not match its bridge/subnet: %#v", exprs)
		}
		if !matchesHostIsolationRule(rule, bridge, subnet) {
			t.Fatalf("current rule did not match host isolation matcher: %#v", exprs)
		}
		if matchesCurrentHostIsolationRule(rule, "br-other", subnet) {
			t.Fatalf("current rule matched a different bridge: %#v", exprs)
		}
		if matchesCurrentHostIsolationRule(rule, bridge, otherSubnet) {
			t.Fatalf("current rule matched a different subnet: %#v", exprs)
		}
	}
}

func TestHostIsolationApplyAndReapplyAreIdempotent(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("172.18.0.0/16")
	_, otherSubnet, _ := net.ParseCIDR("172.19.0.0/16")
	bridge := "br-abc123def456"

	current := expectedHostIsolationRules(bridge, subnet)
	unrelatedCurrent := &nftables.Rule{Exprs: buildDNATIsolationExprs(bridge, otherSubnet)}
	existing := []*nftables.Rule{
		{Exprs: current[0]},
		unrelatedCurrent,
	}

	missing := selectMissingIsolationRules(existing, current)
	if got, want := len(missing), len(current)-1; got != want {
		t.Fatalf("expected %d current inserts, got %d", want, got)
	}
	for _, inserted := range missing {
		if !matchesCurrentHostIsolationRule(&nftables.Rule{Exprs: inserted}, bridge, subnet) {
			t.Fatalf("inserted non-current rule: %#v", inserted)
		}
	}

	reapplied := selectMissingIsolationRules([]*nftables.Rule{
		{Exprs: current[0]},
		{Exprs: current[1]},
		{Exprs: current[2]},
		unrelatedCurrent,
	}, current)
	if len(reapplied) != 0 {
		t.Fatalf("reapply should be idempotent, got inserts=%d", len(reapplied))
	}
}

func TestHostIsolationRemoveDeletesCurrentRules(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("172.18.0.0/16")
	_, otherSubnet, _ := net.ParseCIDR("172.19.0.0/16")
	bridge := "br-abc123def456"

	current := expectedHostIsolationRules(bridge, subnet)
	sameBridgeRules := []*nftables.Rule{
		{Exprs: current[0]},
		{Exprs: current[1]},
		{Exprs: current[2]},
	}
	unrelatedRules := []*nftables.Rule{
		{Exprs: buildHostIsolationExprs("br-other", subnet)},
		{Exprs: buildDNATIsolationExprs("br-other", subnet)},
		{Exprs: buildHostInputIsolationExprs(bridge, otherSubnet)},
	}

	selected := selectIsolationRulesToRemove(append(sameBridgeRules, unrelatedRules...), current)
	if got, want := len(selected), len(sameBridgeRules); got != want {
		t.Fatalf("expected %d selected rules, got %d", want, got)
	}
	selectedSet := map[*nftables.Rule]bool{}
	for _, rule := range selected {
		selectedSet[rule] = true
	}
	for _, rule := range sameBridgeRules {
		if !selectedSet[rule] {
			t.Fatalf("same bridge/subnet rule was not selected: %#v", rule.Exprs)
		}
	}
	for _, rule := range unrelatedRules {
		if selectedSet[rule] {
			t.Fatalf("unrelated bridge/subnet rule was selected: %#v", rule.Exprs)
		}
	}
}

func TestNftIfname(t *testing.T) {
	result := nftIfname("br-test")
	if len(result) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(result))
	}
	// "br-test" is 7 chars + null terminator at index 7.
	if result[7] != 0 {
		t.Fatalf("expected null terminator at index 7, got %d", result[7])
	}
	// Remaining bytes should be zero.
	for i := 8; i < 16; i++ {
		if result[i] != 0 {
			t.Fatalf("expected zero at index %d, got %d", i, result[i])
		}
	}
}
