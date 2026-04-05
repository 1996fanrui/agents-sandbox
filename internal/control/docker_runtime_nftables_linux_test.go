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

	// Expected structure: Meta, Cmp, Payload, Bitwise, Cmp, Fib, Cmp, Verdict = 8 expressions.
	if len(exprs) != 8 {
		t.Fatalf("expected 8 expressions, got %d", len(exprs))
	}

	// Verify expression types in order.
	typeChecks := []string{"Meta", "Cmp", "Payload", "Bitwise", "Cmp", "Fib", "Cmp", "Verdict"}
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

	// Verify fib RTN_LOCAL comparison uses native endian.
	cmpFib := exprs[6].(*expr.Cmp)
	expectedRTNLocal := binaryutil.NativeEndian.PutUint32(rtnLocal)
	if !bytes.Equal(cmpFib.Data, expectedRTNLocal) {
		t.Fatalf("RTN_LOCAL mismatch: got %v, want %v", cmpFib.Data, expectedRTNLocal)
	}

	// Verify DROP verdict.
	verdict := exprs[7].(*expr.Verdict)
	if verdict.Kind != expr.VerdictDrop {
		t.Fatalf("expected VerdictDrop, got %v", verdict.Kind)
	}
}

func TestMatchesHostIsolationRule(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("172.18.0.0/16")
	bridge := "br-abc123def456"

	// Positive: matching rule should be detected.
	matchingRule := &nftables.Rule{
		Exprs: buildHostIsolationExprs(bridge, subnet),
	}
	if !matchesHostIsolationRule(matchingRule, bridge, subnet) {
		t.Fatal("expected matching rule to be detected")
	}

	// Negative: different bridge should not match.
	if matchesHostIsolationRule(matchingRule, "br-other", subnet) {
		t.Fatal("different bridge should not match")
	}

	// Negative: different subnet should not match.
	_, otherSubnet, _ := net.ParseCIDR("10.0.0.0/24")
	if matchesHostIsolationRule(matchingRule, bridge, otherSubnet) {
		t.Fatal("different subnet should not match")
	}

	// Negative: wrong number of expressions.
	shortRule := &nftables.Rule{
		Exprs: []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}},
	}
	if matchesHostIsolationRule(shortRule, bridge, subnet) {
		t.Fatal("rule with wrong expression count should not match")
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
