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

package bindings

import (
	"context"
	"io"

	"github.com/dapr/components-contrib/bindings"
)

// StreamingOutputBinding is an optional interface for output bindings
// that support bidirectional streaming.
// Output bindings can implement this interface to enable streaming
// invocations alongside the standard Invoke method.
type StreamingOutputBinding interface {
	bindings.OutputBinding

	// InvokeStream initiates a streaming invocation of the output binding.
	// It returns a StreamInvoker for bidirectional communication.
	// The caller is responsible for calling Close() on the returned StreamInvoker.
	InvokeStream(ctx context.Context, req *StreamInvokeRequest) (StreamInvoker, error)
}

// StreamInvokeRequest contains the initial request configuration for streaming invocation.
type StreamInvokeRequest struct {
	// Operation is the binding operation to perform (e.g., "create", "delete").
	Operation bindings.OperationKind
	// Metadata contains optional key-value pairs for the binding.
	Metadata map[string]string
}

// StreamInvoker handles bidirectional streaming communication with an output binding.
// It allows sending data chunks to the binding and receiving response chunks.
type StreamInvoker interface {
	// Send sends a data chunk to the binding.
	// Returns io.EOF when the stream has been closed by the binding.
	Send(ctx context.Context, data []byte) error

	// Recv receives a data chunk from the binding.
	// Returns io.EOF when no more data is available.
	Recv(ctx context.Context) ([]byte, error)

	// GetResponseMetadata returns the response metadata from the binding.
	// This is typically available after the first successful Recv() call.
	GetResponseMetadata() map[string]string

	// GetContentType returns the content type of the response.
	// This is typically available after the first successful Recv() call.
	GetContentType() string

	// CloseSend signals that no more data will be sent to the binding.
	// After calling CloseSend, Send() should not be called.
	// Recv() can still be called to receive remaining response data.
	CloseSend() error

	// Close closes the stream and releases resources.
	// Both Send() and Recv() should not be called after Close().
	Close() error
}

// StreamInvokeResponse contains the response data from a streaming invocation.
type StreamInvokeResponse struct {
	// Data is a reader for the response data stream.
	Data io.Reader
	// Metadata contains response metadata from the binding.
	Metadata map[string]string
	// ContentType is the MIME type of the response data.
	ContentType string
}
