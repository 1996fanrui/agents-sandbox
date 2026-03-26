package rawclient

import (
	"io"
	"sync"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// SandboxEventStream is the raw event stream primitive returned by rawclient.
type SandboxEventStream interface {
	Recv() (*agboxv1.SandboxEvent, error)
	Close() error
}

type subscribeSandboxEventsStream interface {
	Recv() (*agboxv1.SandboxEvent, error)
	CloseSend() error
}

type sandboxEventStream struct {
	stream    subscribeSandboxEventsStream
	cancel    func()
	closeOnce sync.Once
	closeErr  error
}

func newSandboxEventStream(stream subscribeSandboxEventsStream, cancel func()) SandboxEventStream {
	return &sandboxEventStream{
		stream: stream,
		cancel: cancel,
	}
}

func (s *sandboxEventStream) Recv() (*agboxv1.SandboxEvent, error) {
	event, err := s.stream.Recv()
	if err != nil {
		s.Close()
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, translateRPCError(err)
	}
	return event, nil
}

func (s *sandboxEventStream) Close() error {
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.stream != nil {
			s.closeErr = s.stream.CloseSend()
		}
	})
	return s.closeErr
}
