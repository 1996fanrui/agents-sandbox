package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestAgentTypeDef_NoPhasesField(t *testing.T) {
	// AT-O3: verify agentTypeDef struct has no "phases" field.
	rt := reflect.TypeOf(agentTypeDef{})
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Name == "phases" {
			t.Fatal("agentTypeDef must not have a 'phases' field")
		}
	}
}

func TestAgentTypeDef_NoExecPhaseType(t *testing.T) {
	// Verify no "execPhase" type exists in agent_types.go source.
	data, err := os.ReadFile("agent_types.go")
	if err != nil {
		t.Fatalf("read agent_types.go: %v", err)
	}
	if strings.Contains(string(data), "execPhase") {
		t.Fatal("agent_types.go must not contain 'execPhase' type")
	}
}
