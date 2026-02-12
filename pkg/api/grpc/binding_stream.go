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

package grpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	diag "github.com/dapr/dapr/pkg/diagnostics"
	"github.com/dapr/dapr/pkg/messages"
	"github.com/dapr/dapr/pkg/messaging"
	commonv1pb "github.com/dapr/dapr/pkg/proto/common/v1"
	runtimev1pb "github.com/dapr/dapr/pkg/proto/runtime/v1"
)

// Timeout for waiting for the first message in the stream for binding requests.
const bindingStreamFirstChunkTimeout = 5 * time.Second

// InvokeBindingAlpha1 handles streaming output binding invocations.
// Both request and response data are streamed, allowing for large payload handling
// without buffering everything in memory.
func (a *api) InvokeBindingAlpha1(stream runtimev1pb.Dapr_InvokeBindingAlpha1Server) error {
	// Get the first message from the caller containing the initial request
	reqProto, err := a.bindingStreamGetFirstChunk(stream)
	if err != nil {
		a.logger.Debug(err)
		return err
	}

	// Validate that the first message contains the initial request
	initialReq := reqProto.GetInitialRequest()
	if initialReq == nil {
		err = messages.ErrBadRequest.WithFormat("first message must contain initial_request")
		a.logger.Debug(err)
		return err
	}

	// Validate binding name
	bindingName := initialReq.GetName()
	if bindingName == "" {
		err = messages.ErrBadRequest.WithFormat("binding name is required")
		a.logger.Debug(err)
		return err
	}

	// Validate operation
	if initialReq.GetOperation() == "" {
		err = messages.ErrBadRequest.WithFormat("operation is required")
		a.logger.Debug(err)
		return err
	}

	// Process the streaming request
	return a.invokeBindingStream(stream, reqProto, bindingName, initialReq)
}

// bindingStreamGetFirstChunk retrieves the first chunk from the stream with a timeout.
func (a *api) bindingStreamGetFirstChunk(stream runtimev1pb.Dapr_InvokeBindingAlpha1Server) (*runtimev1pb.InvokeBindingStreamRequest, error) {
	ctx, cancel := context.WithTimeout(stream.Context(), bindingStreamFirstChunkTimeout)
	defer cancel()

	reqProto := &runtimev1pb.InvokeBindingStreamRequest{}
	firstMsgCh := make(chan error, 1)

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		select {
		case firstMsgCh <- stream.RecvMsg(reqProto):
		case <-ctx.Done():
		}
	}()

	select {
	case <-ctx.Done():
		return nil, messages.ErrBadRequest.WithFormat(
			fmt.Errorf("error waiting for first message: %w", ctx.Err()))
	case err := <-firstMsgCh:
		if err != nil {
			return nil, messages.ErrBadRequest.WithFormat(
				fmt.Errorf("error receiving first message: %w", err))
		}
	}

	return reqProto, nil
}

// invokeBindingStream processes the streaming binding invocation.
func (a *api) invokeBindingStream(
	stream runtimev1pb.Dapr_InvokeBindingAlpha1Server,
	reqProto *runtimev1pb.InvokeBindingStreamRequest,
	bindingName string,
	initialReq *runtimev1pb.InvokeBindingStreamRequestInitial,
) error {
	// Create a pipe for incoming data
	inReader, inWriter := io.Pipe()

	ctx := stream.Context()

	// Process incoming stream data in background
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer inWriter.Close()

		var (
			readSeq   uint64
			expectSeq uint64
			payload   *commonv1pb.StreamPayload
			readErr   error
		)

		// Process all chunks until EOF or error
		for {
			// Check for context cancellation
			if ctx.Err() != nil {
				inWriter.CloseWithError(ctx.Err())
				return
			}

			// Get the payload from the current message
			payload = reqProto.GetPayload()
			if payload != nil {
				readSeq, readErr = messaging.ReadChunk(payload, inWriter)
				if readErr != nil {
					inWriter.CloseWithError(readErr)
					return
				}
				if readSeq != expectSeq {
					inWriter.CloseWithError(fmt.Errorf("invalid sequence number: got %d, expected %d", readSeq, expectSeq))
					return
				}
				expectSeq++
			}

			// Read the next message
			reqProto = &runtimev1pb.InvokeBindingStreamRequest{}
			if err := stream.RecvMsg(reqProto); err != nil {
				if errors.Is(err, io.EOF) {
					// Normal end of stream
					return
				}
				inWriter.CloseWithError(err)
				return
			}

			// Validate that subsequent messages don't contain initial_request
			if reqProto.GetInitialRequest() != nil {
				inWriter.CloseWithError(errors.New("initial_request found in non-leading message"))
				return
			}
		}
	}()

	// Call the binding processor to handle the streaming invocation
	start := time.Now()
	var err error
	if a.sendToOutputBindingStreamFn != nil {
		err = a.sendToOutputBindingStreamFn(ctx, stream, bindingName, initialReq, inReader)
	} else if a.processor == nil {
		err = errors.New("binding streaming not configured")
	} else {
		err = a.processor.Binding().SendToOutputBindingStream(ctx, stream, bindingName, initialReq, inReader)
	}
	elapsed := diag.ElapsedSince(start)

	// Record metrics for the streaming binding operation
	diag.DefaultComponentMonitoring.OutputBindingEvent(context.Background(), bindingName, initialReq.GetOperation(), err == nil, elapsed)

	return err
}
