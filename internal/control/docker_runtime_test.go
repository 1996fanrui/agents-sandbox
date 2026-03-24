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
