package rawclient

import (
	"context"
	"net"
	"os"
	"time"

	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultTimeout = 5 * time.Second

// DefaultSocketPath resolves the daemon socket path through internal platform.
func DefaultSocketPath() (string, error) {
	return platform.SocketPath(os.LookupEnv)
}

// Dial creates a gRPC client connection over Unix socket.
func Dial(socketPath string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	options := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			dialer := net.Dialer{}
			return dialer.DialContext(ctx, "unix", socketPath)
		}),
	}
	options = append(options, opts...)
	return grpc.NewClient("passthrough:///agents-sandbox", options...)
}

// CallOption configures RawClient behavior.
type CallOption func(*callOptions)

type callOptions struct {
	timeout     time.Duration
	dialOptions []grpc.DialOption
}

// WithTimeout sets the timeout used when ctx does not carry a deadline.
func WithTimeout(d time.Duration) CallOption {
	return func(opts *callOptions) {
		opts.timeout = d
	}
}

// WithDialOptions appends additional gRPC dial options.
func WithDialOptions(dialOptions ...grpc.DialOption) CallOption {
	return func(opts *callOptions) {
		opts.dialOptions = append(opts.dialOptions, dialOptions...)
	}
}

func newDefaultCallOptions() callOptions {
	return callOptions{
		timeout: defaultTimeout,
	}
}
