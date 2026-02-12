/*
Copyright 2025 The Dapr Authors
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package binding

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	compv1pb "github.com/dapr/dapr/pkg/proto/components/v1"
	"github.com/dapr/dapr/tests/integration/framework/os"
	"github.com/dapr/dapr/tests/integration/framework/socket"
)

// OutputBinding is a test framework component that implements the pluggable
// output binding gRPC interface with streaming support.
type OutputBinding struct {
	listener   net.Listener
	socketName string
	socket     *socket.Socket
	srvErrCh   chan error
	stopCh     chan struct{}
	handler    OutputBindingHandler
}

// OutputBindingHandler defines how the output binding processes requests.
type OutputBindingHandler interface {
	// Invoke handles non-streaming invoke requests.
	Invoke(ctx context.Context, req *compv1pb.InvokeRequest) (*compv1pb.InvokeResponse, error)
	// InvokeStream handles streaming invoke requests. Return nil to use default echo behavior.
	InvokeStream(stream compv1pb.OutputBinding_InvokeStreamServer) error
}

// EchoOutputBindingHandler is a simple handler that echoes back the data received.
type EchoOutputBindingHandler struct {
	// ChunkSize is the size of chunks to use when echoing back data. Default is 32KB.
	ChunkSize int
}

func (h *EchoOutputBindingHandler) Invoke(ctx context.Context, req *compv1pb.InvokeRequest) (*compv1pb.InvokeResponse, error) {
	return &compv1pb.InvokeResponse{
		Data:        req.GetData(),
		Metadata:    req.GetMetadata(),
		ContentType: "application/octet-stream",
	}, nil
}

func (h *EchoOutputBindingHandler) InvokeStream(stream compv1pb.OutputBinding_InvokeStreamServer) error {
	chunkSize := h.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 32 * 1024 // 32KB default
	}

	// Receive initial request
	msg, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("failed to receive initial request: %w", err)
	}

	initReq := msg.GetInitialRequest()
	if initReq == nil {
		return fmt.Errorf("first message must be initial request")
	}

	// Send initial response
	if err := stream.Send(&compv1pb.InvokeStreamResponse{
		InvokeStreamResponseType: &compv1pb.InvokeStreamResponse_InitialResponse{
			InitialResponse: &compv1pb.InvokeStreamResponseInitial{
				Metadata:    initReq.GetMetadata(),
				ContentType: "application/octet-stream",
			},
		},
	}); err != nil {
		return fmt.Errorf("failed to send initial response: %w", err)
	}

	// Echo back data in chunks
	var respSeq uint64 = 0
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to receive: %w", err)
		}

		// Component proto uses GetData() directly, not GetPayload()
		data := msg.GetData()
		if len(data) == 0 {
			continue
		}

		// Send data back in chunks
		for offset := 0; offset < len(data); offset += chunkSize {
			end := offset + chunkSize
			if end > len(data) {
				end = len(data)
			}
			chunk := data[offset:end]

			if err := stream.Send(&compv1pb.InvokeStreamResponse{
				InvokeStreamResponseType: &compv1pb.InvokeStreamResponse_Data{
					Data: chunk,
				},
				Seq: respSeq,
			}); err != nil {
				return fmt.Errorf("failed to send chunk: %w", err)
			}
			respSeq++
		}
	}
}

type outputComponent struct {
	handler OutputBindingHandler
}

func (c *outputComponent) Init(ctx context.Context, req *compv1pb.OutputBindingInitRequest) (*compv1pb.OutputBindingInitResponse, error) {
	return &compv1pb.OutputBindingInitResponse{}, nil
}

func (c *outputComponent) Invoke(ctx context.Context, req *compv1pb.InvokeRequest) (*compv1pb.InvokeResponse, error) {
	return c.handler.Invoke(ctx, req)
}

func (c *outputComponent) InvokeStream(stream compv1pb.OutputBinding_InvokeStreamServer) error {
	return c.handler.InvokeStream(stream)
}

func (c *outputComponent) ListOperations(ctx context.Context, req *compv1pb.ListOperationsRequest) (*compv1pb.ListOperationsResponse, error) {
	return &compv1pb.ListOperationsResponse{
		Operations: []string{"create", "get", "delete", "list"},
	}, nil
}

func (c *outputComponent) Ping(ctx context.Context, req *compv1pb.PingRequest) (*compv1pb.PingResponse, error) {
	return &compv1pb.PingResponse{}, nil
}

// OutputBindingOption is a function that configures an OutputBinding.
type OutputBindingOption func(*outputBindingOptions)

type outputBindingOptions struct {
	socket  *socket.Socket
	handler OutputBindingHandler
}

// WithOutputSocket sets the socket for the output binding.
func WithOutputSocket(s *socket.Socket) OutputBindingOption {
	return func(o *outputBindingOptions) {
		o.socket = s
	}
}

// WithOutputHandler sets the handler for the output binding.
func WithOutputHandler(h OutputBindingHandler) OutputBindingOption {
	return func(o *outputBindingOptions) {
		o.handler = h
	}
}

// NewOutputBinding creates a new streaming output binding for integration tests.
func NewOutputBinding(t *testing.T, fopts ...OutputBindingOption) *OutputBinding {
	t.Helper()

	os.SkipWindows(t)

	opts := outputBindingOptions{
		socket:  socket.New(t),
		handler: &EchoOutputBindingHandler{},
	}
	for _, fopt := range fopts {
		fopt(&opts)
	}

	require.NotNil(t, opts.socket)

	// Start the listener in New, so we can sit on the path immediately
	socketFile := opts.socket.File(t)
	listener, err := net.Listen("unix", socketFile.Filename())
	require.NoError(t, err)

	return &OutputBinding{
		listener:   listener,
		socketName: socketFile.Name(),
		socket:     opts.socket,
		handler:    opts.handler,
		srvErrCh:   make(chan error),
		stopCh:     make(chan struct{}),
	}
}

func (b *OutputBinding) Run(t *testing.T, ctx context.Context) {
	server := grpc.NewServer()
	compv1pb.RegisterOutputBindingServer(server, &outputComponent{handler: b.handler})
	reflection.Register(server)

	go func() {
		b.srvErrCh <- server.Serve(b.listener)
	}()

	go func() {
		<-b.stopCh
		server.GracefulStop()
	}()
}

func (b *OutputBinding) Cleanup(t *testing.T) {
	close(b.stopCh)
	require.NoError(t, <-b.srvErrCh)
}

func (b *OutputBinding) SocketName() string {
	return b.socketName
}

func (b *OutputBinding) Socket() *socket.Socket {
	return b.socket
}
