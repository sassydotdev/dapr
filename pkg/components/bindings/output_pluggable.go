/*
Copyright 2022 The Dapr Authors
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

package bindings

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/dapr/components-contrib/bindings"
	"github.com/dapr/dapr/pkg/components/pluggable"
	proto "github.com/dapr/dapr/pkg/proto/components/v1"
	"github.com/dapr/kit/logger"
)

// grpcOutputBinding is a implementation of a outputbinding over a gRPC Protocol.
// It implements both bindings.OutputBinding and StreamingOutputBinding interfaces.
type grpcOutputBinding struct {
	*pluggable.GRPCConnector[proto.OutputBindingClient]
	bindings.OutputBinding
	operations []bindings.OperationKind
	logger     logger.Logger
}

// Init initializes the grpc outputbinding passing out the metadata to the grpc component.
func (b *grpcOutputBinding) Init(ctx context.Context, metadata bindings.Metadata) error {
	if err := b.Dial(metadata.Name); err != nil {
		return err
	}

	protoMetadata := &proto.MetadataRequest{
		Properties: metadata.Properties,
	}

	_, err := b.Client.Init(b.Context, &proto.OutputBindingInitRequest{
		Metadata: protoMetadata,
	})
	if err != nil {
		return err
	}

	operations, err := b.Client.ListOperations(b.Context, &proto.ListOperationsRequest{})
	if err != nil {
		return err
	}

	operationsList := operations.GetOperations()
	ops := make([]bindings.OperationKind, len(operationsList))

	for idx, op := range operationsList {
		ops[idx] = bindings.OperationKind(op)
	}
	b.operations = ops

	return nil
}

// Operations list bindings operations.
func (b *grpcOutputBinding) Operations() []bindings.OperationKind {
	return b.operations
}

// Invoke the component with the given payload, metadata and operation.
func (b *grpcOutputBinding) Invoke(ctx context.Context, req *bindings.InvokeRequest) (*bindings.InvokeResponse, error) {
	resp, err := b.Client.Invoke(ctx, &proto.InvokeRequest{
		Data:      req.Data,
		Metadata:  req.Metadata,
		Operation: string(req.Operation),
	})
	if err != nil {
		return nil, err
	}

	var contentType *string
	if len(resp.GetContentType()) != 0 {
		contentType = &resp.ContentType
	}

	return &bindings.InvokeResponse{
		Data:        resp.GetData(),
		Metadata:    resp.GetMetadata(),
		ContentType: contentType,
	}, nil
}

// InvokeStream initiates a streaming invocation to the output binding component.
// It implements the StreamingOutputBinding interface.
func (b *grpcOutputBinding) InvokeStream(ctx context.Context, req *StreamInvokeRequest) (StreamInvoker, error) {
	stream, err := b.Client.InvokeStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create stream: %w", err)
	}

	// Send the initial request with operation and metadata
	if err := stream.Send(&proto.InvokeStreamRequest{
		InvokeStreamRequestType: &proto.InvokeStreamRequest_InitialRequest{
			InitialRequest: &proto.InvokeStreamRequestInitial{
				Metadata:  req.Metadata,
				Operation: string(req.Operation),
			},
		},
		Seq: 0,
	}); err != nil {
		return nil, fmt.Errorf("failed to send initial request: %w", err)
	}

	return &grpcStreamInvoker{
		stream:   stream,
		logger:   b.logger,
		safeSend: &sync.Mutex{},
		sendSeq:  1, // Start at 1 since initial request used 0
	}, nil
}

// grpcStreamInvoker implements StreamInvoker for gRPC-based output bindings.
type grpcStreamInvoker struct {
	stream      proto.OutputBinding_InvokeStreamClient
	logger      logger.Logger
	safeSend    *sync.Mutex
	metadata    map[string]string
	contentType string
	sendSeq     uint64
	recvSeq     uint64
	initialized atomic.Bool
	closed      atomic.Bool
}

// Send sends a data chunk to the binding.
func (s *grpcStreamInvoker) Send(ctx context.Context, data []byte) error {
	if s.closed.Load() {
		return io.ErrClosedPipe
	}

	s.safeSend.Lock()
	defer s.safeSend.Unlock()

	err := s.stream.Send(&proto.InvokeStreamRequest{
		InvokeStreamRequestType: &proto.InvokeStreamRequest_Data{
			Data: data,
		},
		Seq: s.sendSeq,
	})
	if err != nil {
		return err
	}

	s.sendSeq++
	return nil
}

// Recv receives a data chunk from the binding.
func (s *grpcStreamInvoker) Recv(ctx context.Context) ([]byte, error) {
	if s.closed.Load() {
		return nil, io.EOF
	}

	resp, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}

	// Handle initial response
	if initialResp := resp.GetInitialResponse(); initialResp != nil {
		s.metadata = initialResp.GetMetadata()
		s.contentType = initialResp.GetContentType()
		s.initialized.Store(true)
		// Continue to get the next message which should contain data
		resp, err = s.stream.Recv()
		if err != nil {
			return nil, err
		}
	}

	// Validate sequence number
	expectedSeq := s.recvSeq
	if resp.GetSeq() != expectedSeq {
		return nil, fmt.Errorf("sequence mismatch: expected %d, got %d", expectedSeq, resp.GetSeq())
	}
	s.recvSeq++

	return resp.GetData(), nil
}

// GetResponseMetadata returns the response metadata from the binding.
func (s *grpcStreamInvoker) GetResponseMetadata() map[string]string {
	return s.metadata
}

// GetContentType returns the content type of the response.
func (s *grpcStreamInvoker) GetContentType() string {
	return s.contentType
}

// CloseSend signals that no more data will be sent to the binding.
func (s *grpcStreamInvoker) CloseSend() error {
	return s.stream.CloseSend()
}

// Close closes the stream and releases resources.
func (s *grpcStreamInvoker) Close() error {
	s.closed.Store(true)
	return nil
}

func (b *grpcOutputBinding) Close() error {
	return b.GRPCConnector.Close()
}

// outputFromConnector creates a new GRPC outputbinding using the given underlying connector.
func outputFromConnector(l logger.Logger, connector *pluggable.GRPCConnector[proto.OutputBindingClient]) *grpcOutputBinding {
	return &grpcOutputBinding{
		GRPCConnector: connector,
		logger:        l,
	}
}

// NewGRPCOutputBinding creates a new grpc outputbinding using the given socket factory.
func NewGRPCOutputBinding(l logger.Logger, socket string) *grpcOutputBinding {
	return outputFromConnector(l, pluggable.NewGRPCConnector(socket, proto.NewOutputBindingClient))
}

// newGRPCOutputBinding creates a new output binding for the given pluggable component.
func newGRPCOutputBinding(dialer pluggable.GRPCConnectionDialer) func(l logger.Logger) bindings.OutputBinding {
	return func(l logger.Logger) bindings.OutputBinding {
		return outputFromConnector(l, pluggable.NewGRPCConnectorWithDialer(dialer, proto.NewOutputBindingClient))
	}
}

func init() {
	//nolint:nosnakecase
	pluggable.AddServiceDiscoveryCallback(proto.OutputBinding_ServiceDesc.ServiceName, func(name string, dialer pluggable.GRPCConnectionDialer) {
		DefaultRegistry.RegisterOutputBinding(newGRPCOutputBinding(dialer), name)
	})
}
