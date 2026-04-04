package control

import (
	"os"
	"strconv"
	"testing"
)

func TestPrimaryContainerEnvironmentIncludesHostIdentity(t *testing.T) {
	environment := primaryContainerEnvironment(nil)

	if got, want := environment["HOST_UID"], strconv.Itoa(os.Getuid()); got != want {
		t.Fatalf("unexpected HOST_UID: got %q want %q", got, want)
	}
	if got, want := environment["HOST_GID"], strconv.Itoa(os.Getgid()); got != want {
		t.Fatalf("unexpected HOST_GID: got %q want %q", got, want)
	}
	if _, exists := environment["SSH_AUTH_SOCK"]; exists {
		t.Fatalf("unexpected SSH_AUTH_SOCK without ssh-agent mount: %#v", environment)
	}
	if _, exists := environment["PULSE_SERVER"]; exists {
		t.Fatalf("unexpected PULSE_SERVER without pulse-audio mount: %#v", environment)
	}
}

func TestPrimaryContainerEnvironmentIncludesPulseServerWhenMounted(t *testing.T) {
	environment := primaryContainerEnvironment([]dockerMount{
		{Target: "/pulse-audio"},
	})
	if got, want := environment["PULSE_SERVER"], "unix:/pulse-audio"; got != want {
		t.Fatalf("unexpected PULSE_SERVER: got %q want %q", got, want)
	}
}

func TestPrimaryContainerEnvironmentIncludesSshAuthSockWhenMounted(t *testing.T) {
	environment := primaryContainerEnvironment([]dockerMount{
		{Target: "/ssh-agent"},
	})

	if got, want := environment["SSH_AUTH_SOCK"], "/ssh-agent"; got != want {
		t.Fatalf("unexpected SSH_AUTH_SOCK: got %q want %q", got, want)
	}
	if got, want := environment["HOST_UID"], strconv.Itoa(os.Getuid()); got != want {
		t.Fatalf("unexpected HOST_UID: got %q want %q", got, want)
	}
	if got, want := environment["HOST_GID"], strconv.Itoa(os.Getgid()); got != want {
		t.Fatalf("unexpected HOST_GID: got %q want %q", got, want)
	}
}
