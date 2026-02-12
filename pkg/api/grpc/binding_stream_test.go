//go:build unit
// +build unit

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
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1pb "github.com/dapr/dapr/pkg/proto/common/v1"
	runtimev1pb "github.com/dapr/dapr/pkg/proto/runtime/v1"
	"github.com/dapr/kit/logger"
)

func TestInvokeBindingAlpha1_ValidationErrors(t *testing.T) {
	tests := []struct {
		name          string
		initialReq    *runtimev1pb.InvokeBindingStreamRequestInitial
		expectedError string
		expectedCode  codes.Code
	}{
		{
			name:          "missing initial request",
			initialReq:    nil,
			expectedError: "first message must contain initial_request",
			expectedCode:  codes.InvalidArgument,
		},
		{
			name: "missing binding name",
			initialReq: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "",
				Operation: "create",
			},
			expectedError: "binding name is required",
			expectedCode:  codes.InvalidArgument,
		},
		{
			name: "missing operation",
			initialReq: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "test-binding",
				Operation: "",
			},
			expectedError: "operation is required",
			expectedCode:  codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &api{
				logger: logger.NewLogger("test"),
			}
			lis := startTestServerAPI(t, srv)

			clientConn := createTestClient(lis)
			defer clientConn.Close()

			client := runtimev1pb.NewDaprClient(clientConn)
			stream, err := client.InvokeBindingAlpha1(t.Context())
			require.NoError(t, err)

			// Build the first message
			var msg *runtimev1pb.InvokeBindingStreamRequest
			if tt.initialReq != nil {
				msg = &runtimev1pb.InvokeBindingStreamRequest{
					InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
						InitialRequest: tt.initialReq,
					},
				}
			} else {
				// Send a payload message instead of initial request
				msg = &runtimev1pb.InvokeBindingStreamRequest{
					InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_Payload{
						Payload: &commonv1pb.StreamPayload{
							Data: []byte("test"),
							Seq:  0,
						},
					},
				}
			}

			err = stream.Send(msg)
			require.NoError(t, err)

			// Close send side and receive error
			err = stream.CloseSend()
			require.NoError(t, err)

			_, err = stream.Recv()
			require.Error(t, err)
			s, ok := status.FromError(err)
			assert.True(t, ok)
			assert.Equal(t, tt.expectedCode, s.Code())
			assert.Contains(t, s.Message(), tt.expectedError)
		})
	}
}

func TestInvokeBindingAlpha1_SuccessWithSingleChunk(t *testing.T) {
	var receivedName string
	var receivedOp string
	var receivedData []byte

	srv := &api{
		logger: logger.NewLogger("test"),
		sendToOutputBindingStreamFn: func(ctx context.Context, stream runtimev1pb.Dapr_InvokeBindingAlpha1Server, name string, req *runtimev1pb.InvokeBindingStreamRequestInitial, data io.Reader) error {
			receivedName = name
			receivedOp = req.GetOperation()

			var err error
			receivedData, err = io.ReadAll(data)
			if err != nil {
				return err
			}

			// Send initial response
			if err := stream.Send(&runtimev1pb.InvokeBindingStreamResponse{
				InvokeBindingStreamResponseType: &runtimev1pb.InvokeBindingStreamResponse_InitialResponse{
					InitialResponse: &runtimev1pb.InvokeBindingStreamResponseInitial{
						Metadata: map[string]string{"resp-key": "resp-value"},
					},
				},
			}); err != nil {
				return err
			}

			// Send response data
			return stream.Send(&runtimev1pb.InvokeBindingStreamResponse{
				InvokeBindingStreamResponseType: &runtimev1pb.InvokeBindingStreamResponse_Payload{
					Payload: &commonv1pb.StreamPayload{
						Data: []byte("response-data"),
						Seq:  0,
					},
				},
			})
		},
	}
	lis := startTestServerAPI(t, srv)

	clientConn := createTestClient(lis)
	defer clientConn.Close()

	client := runtimev1pb.NewDaprClient(clientConn)
	stream, err := client.InvokeBindingAlpha1(t.Context())
	require.NoError(t, err)

	// Send initial request with data
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "test-binding",
				Operation: "create",
				Metadata:  map[string]string{"key": "value"},
			},
		},
	})
	require.NoError(t, err)

	// Send data chunk
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_Payload{
			Payload: &commonv1pb.StreamPayload{
				Data: []byte("test-data"),
				Seq:  0,
			},
		},
	})
	require.NoError(t, err)

	// Close send side
	err = stream.CloseSend()
	require.NoError(t, err)

	// Receive responses
	var responses []*runtimev1pb.InvokeBindingStreamResponse
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		responses = append(responses, resp)
	}

	// Verify
	assert.Equal(t, "test-binding", receivedName)
	assert.Equal(t, "create", receivedOp)
	assert.Equal(t, []byte("test-data"), receivedData)

	require.Len(t, responses, 2)
	assert.NotNil(t, responses[0].GetInitialResponse())
	assert.Equal(t, "resp-value", responses[0].GetInitialResponse().GetMetadata()["resp-key"])
	assert.Equal(t, []byte("response-data"), responses[1].GetPayload().GetData())
}

func TestInvokeBindingAlpha1_MultipleChunks(t *testing.T) {
	var receivedData []byte

	srv := &api{
		logger: logger.NewLogger("test"),
		sendToOutputBindingStreamFn: func(ctx context.Context, stream runtimev1pb.Dapr_InvokeBindingAlpha1Server, name string, req *runtimev1pb.InvokeBindingStreamRequestInitial, data io.Reader) error {
			var err error
			receivedData, err = io.ReadAll(data)
			if err != nil {
				return err
			}

			// Send initial response
			if err := stream.Send(&runtimev1pb.InvokeBindingStreamResponse{
				InvokeBindingStreamResponseType: &runtimev1pb.InvokeBindingStreamResponse_InitialResponse{
					InitialResponse: &runtimev1pb.InvokeBindingStreamResponseInitial{},
				},
			}); err != nil {
				return err
			}

			// Echo back the data in multiple chunks
			chunkSize := len(receivedData) / 3
			for i := range 3 {
				start := i * chunkSize
				end := start + chunkSize
				if i == 2 {
					end = len(receivedData)
				}
				if sendErr := stream.Send(&runtimev1pb.InvokeBindingStreamResponse{
					InvokeBindingStreamResponseType: &runtimev1pb.InvokeBindingStreamResponse_Payload{
						Payload: &commonv1pb.StreamPayload{
							Data: receivedData[start:end],
							Seq:  uint64(i), //nolint:gosec
						},
					},
				}); sendErr != nil {
					return sendErr
				}
			}
			return nil
		},
	}
	lis := startTestServerAPI(t, srv)

	clientConn := createTestClient(lis)
	defer clientConn.Close()

	client := runtimev1pb.NewDaprClient(clientConn)
	stream, err := client.InvokeBindingAlpha1(t.Context())
	require.NoError(t, err)

	// Send initial request
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "test-binding",
				Operation: "create",
			},
		},
	})
	require.NoError(t, err)

	// Send multiple data chunks
	chunks := []string{"chunk1-", "chunk2-", "chunk3"}
	for i, chunk := range chunks {
		err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
			InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_Payload{
				Payload: &commonv1pb.StreamPayload{
					Data: []byte(chunk),
					Seq:  uint64(i), //nolint:gosec
				},
			},
		})
		require.NoError(t, err)
	}

	err = stream.CloseSend()
	require.NoError(t, err)

	// Receive all responses
	var responseData bytes.Buffer
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if payload := resp.GetPayload(); payload != nil {
			responseData.Write(payload.GetData())
		}
	}

	// Verify received data
	assert.Equal(t, "chunk1-chunk2-chunk3", string(receivedData))
	assert.Equal(t, "chunk1-chunk2-chunk3", responseData.String())
}

func TestInvokeBindingAlpha1_LargePayload(t *testing.T) {
	// Generate 1MB of random data
	largeData := make([]byte, 1<<20)
	_, err := io.ReadFull(rand.Reader, largeData)
	require.NoError(t, err)

	var receivedData []byte

	srv := &api{
		logger: logger.NewLogger("test"),
		sendToOutputBindingStreamFn: func(ctx context.Context, stream runtimev1pb.Dapr_InvokeBindingAlpha1Server, name string, req *runtimev1pb.InvokeBindingStreamRequestInitial, data io.Reader) error {
			var readErr error
			receivedData, readErr = io.ReadAll(data)
			if readErr != nil {
				return readErr
			}

			// Send initial response
			if sendErr := stream.Send(&runtimev1pb.InvokeBindingStreamResponse{
				InvokeBindingStreamResponseType: &runtimev1pb.InvokeBindingStreamResponse_InitialResponse{
					InitialResponse: &runtimev1pb.InvokeBindingStreamResponseInitial{},
				},
			}); sendErr != nil {
				return sendErr
			}

			// Send response with size confirmation
			return stream.Send(&runtimev1pb.InvokeBindingStreamResponse{
				InvokeBindingStreamResponseType: &runtimev1pb.InvokeBindingStreamResponse_Payload{
					Payload: &commonv1pb.StreamPayload{
						Data: []byte(fmt.Sprintf("received %d bytes", len(receivedData))),
						Seq:  0,
					},
				},
			})
		},
	}
	lis := startTestServerAPI(t, srv)

	clientConn := createTestClient(lis)
	defer clientConn.Close()

	client := runtimev1pb.NewDaprClient(clientConn)
	stream, err := client.InvokeBindingAlpha1(t.Context())
	require.NoError(t, err)

	// Send initial request
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "test-binding",
				Operation: "create",
			},
		},
	})
	require.NoError(t, err)

	// Send large data in chunks
	chunkSize := 64 * 1024 // 64KB chunks
	var seq uint64
	for i := 0; i < len(largeData); i += chunkSize {
		end := i + chunkSize
		if end > len(largeData) {
			end = len(largeData)
		}
		err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
			InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_Payload{
				Payload: &commonv1pb.StreamPayload{
					Data: largeData[i:end],
					Seq:  seq,
				},
			},
		})
		require.NoError(t, err)
		seq++
	}

	err = stream.CloseSend()
	require.NoError(t, err)

	// Drain responses
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}

	// Verify
	assert.Equal(t, len(largeData), len(receivedData))
	assert.True(t, bytes.Equal(largeData, receivedData))
}

func TestInvokeBindingAlpha1_InvalidSequenceNumber(t *testing.T) {
	srv := &api{
		logger: logger.NewLogger("test"),
		sendToOutputBindingStreamFn: func(ctx context.Context, stream runtimev1pb.Dapr_InvokeBindingAlpha1Server, name string, req *runtimev1pb.InvokeBindingStreamRequestInitial, data io.Reader) error {
			// Try to read - should get error due to invalid sequence
			_, err := io.ReadAll(data)
			return err
		},
	}
	lis := startTestServerAPI(t, srv)

	clientConn := createTestClient(lis)
	defer clientConn.Close()

	client := runtimev1pb.NewDaprClient(clientConn)
	stream, err := client.InvokeBindingAlpha1(t.Context())
	require.NoError(t, err)

	// Send initial request
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "test-binding",
				Operation: "create",
			},
		},
	})
	require.NoError(t, err)

	// Send chunk with wrong sequence number (skip 0, send 1)
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_Payload{
			Payload: &commonv1pb.StreamPayload{
				Data: []byte("test"),
				Seq:  1, // Should be 0
			},
		},
	})
	require.NoError(t, err)

	err = stream.CloseSend()
	require.NoError(t, err)

	// Should receive error
	_, err = stream.Recv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sequence")
}

func TestInvokeBindingAlpha1_InitialRequestInNonLeadingMessage(t *testing.T) {
	srv := &api{
		logger: logger.NewLogger("test"),
		sendToOutputBindingStreamFn: func(ctx context.Context, stream runtimev1pb.Dapr_InvokeBindingAlpha1Server, name string, req *runtimev1pb.InvokeBindingStreamRequestInitial, data io.Reader) error {
			// Try to read - should get error
			_, err := io.ReadAll(data)
			return err
		},
	}
	lis := startTestServerAPI(t, srv)

	clientConn := createTestClient(lis)
	defer clientConn.Close()

	client := runtimev1pb.NewDaprClient(clientConn)
	stream, err := client.InvokeBindingAlpha1(t.Context())
	require.NoError(t, err)

	// Send valid initial request
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "test-binding",
				Operation: "create",
			},
		},
	})
	require.NoError(t, err)

	// Send another initial request (invalid)
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "another-binding",
				Operation: "create",
			},
		},
	})
	require.NoError(t, err)

	err = stream.CloseSend()
	require.NoError(t, err)

	// Should receive error
	_, err = stream.Recv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initial_request found in non-leading message")
}

func TestInvokeBindingAlpha1_TimeoutWaitingForFirstChunk(t *testing.T) {
	srv := &api{
		logger: logger.NewLogger("test"),
	}
	lis := startTestServerAPI(t, srv)

	clientConn := createTestClient(lis)
	defer clientConn.Close()

	client := runtimev1pb.NewDaprClient(clientConn)
	stream, err := client.InvokeBindingAlpha1(t.Context())
	require.NoError(t, err)

	// Don't send anything, just wait for timeout
	start := time.Now()
	_, err = stream.Recv()
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "error waiting for first message")
	assert.GreaterOrEqual(t, elapsed, bindingStreamFirstChunkTimeout)
}

func TestInvokeBindingAlpha1_ContextCancellation(t *testing.T) {
	// Create a context that we'll cancel
	ctx, cancel := context.WithCancel(t.Context())

	srv := &api{
		logger: logger.NewLogger("test"),
		sendToOutputBindingStreamFn: func(ctx context.Context, stream runtimev1pb.Dapr_InvokeBindingAlpha1Server, name string, req *runtimev1pb.InvokeBindingStreamRequestInitial, data io.Reader) error {
			// Wait a bit then try to read - context should be cancelled
			time.Sleep(100 * time.Millisecond)
			_, err := io.ReadAll(data)
			return err
		},
	}
	lis := startTestServerAPI(t, srv)

	clientConn := createTestClient(lis)
	defer clientConn.Close()

	client := runtimev1pb.NewDaprClient(clientConn)
	stream, err := client.InvokeBindingAlpha1(ctx)
	require.NoError(t, err)

	// Send initial request
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "test-binding",
				Operation: "create",
			},
		},
	})
	require.NoError(t, err)

	// Cancel the context
	cancel()

	// Try to receive - should fail due to cancellation
	_, err = stream.Recv()
	require.Error(t, err)
}

func TestInvokeBindingAlpha1_ProcessorError(t *testing.T) {
	expectedErr := errors.New("binding processing failed")

	srv := &api{
		logger: logger.NewLogger("test"),
		sendToOutputBindingStreamFn: func(ctx context.Context, stream runtimev1pb.Dapr_InvokeBindingAlpha1Server, name string, req *runtimev1pb.InvokeBindingStreamRequestInitial, data io.Reader) error {
			return expectedErr
		},
	}
	lis := startTestServerAPI(t, srv)

	clientConn := createTestClient(lis)
	defer clientConn.Close()

	client := runtimev1pb.NewDaprClient(clientConn)
	stream, err := client.InvokeBindingAlpha1(t.Context())
	require.NoError(t, err)

	// Send initial request
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "test-binding",
				Operation: "create",
			},
		},
	})
	require.NoError(t, err)

	err = stream.CloseSend()
	require.NoError(t, err)

	// Should receive error from processor
	_, err = stream.Recv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "binding processing failed")
}

func TestInvokeBindingAlpha1_EmptyPayload(t *testing.T) {
	var receivedData []byte

	srv := &api{
		logger: logger.NewLogger("test"),
		sendToOutputBindingStreamFn: func(ctx context.Context, stream runtimev1pb.Dapr_InvokeBindingAlpha1Server, name string, req *runtimev1pb.InvokeBindingStreamRequestInitial, data io.Reader) error {
			var err error
			receivedData, err = io.ReadAll(data)
			if err != nil {
				return err
			}

			// Send initial response only
			return stream.Send(&runtimev1pb.InvokeBindingStreamResponse{
				InvokeBindingStreamResponseType: &runtimev1pb.InvokeBindingStreamResponse_InitialResponse{
					InitialResponse: &runtimev1pb.InvokeBindingStreamResponseInitial{
						Metadata: map[string]string{"status": "ok"},
					},
				},
			})
		},
	}
	lis := startTestServerAPI(t, srv)

	clientConn := createTestClient(lis)
	defer clientConn.Close()

	client := runtimev1pb.NewDaprClient(clientConn)
	stream, err := client.InvokeBindingAlpha1(t.Context())
	require.NoError(t, err)

	// Send initial request only, no payload
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "test-binding",
				Operation: "create",
			},
		},
	})
	require.NoError(t, err)

	err = stream.CloseSend()
	require.NoError(t, err)

	// Receive response
	resp, err := stream.Recv()
	require.NoError(t, err)
	assert.NotNil(t, resp.GetInitialResponse())
	assert.Equal(t, "ok", resp.GetInitialResponse().GetMetadata()["status"])

	// Verify empty data
	assert.Empty(t, receivedData)
}

func TestInvokeBindingAlpha1_MetadataPropagation(t *testing.T) {
	var receivedMetadata map[string]string

	srv := &api{
		logger: logger.NewLogger("test"),
		sendToOutputBindingStreamFn: func(ctx context.Context, stream runtimev1pb.Dapr_InvokeBindingAlpha1Server, name string, req *runtimev1pb.InvokeBindingStreamRequestInitial, data io.Reader) error {
			receivedMetadata = req.GetMetadata()

			// Echo metadata back in response
			return stream.Send(&runtimev1pb.InvokeBindingStreamResponse{
				InvokeBindingStreamResponseType: &runtimev1pb.InvokeBindingStreamResponse_InitialResponse{
					InitialResponse: &runtimev1pb.InvokeBindingStreamResponseInitial{
						Metadata: receivedMetadata,
					},
				},
			})
		},
	}
	lis := startTestServerAPI(t, srv)

	clientConn := createTestClient(lis)
	defer clientConn.Close()

	client := runtimev1pb.NewDaprClient(clientConn)
	stream, err := client.InvokeBindingAlpha1(t.Context())
	require.NoError(t, err)

	expectedMetadata := map[string]string{
		"key1":        "value1",
		"key2":        "value2",
		"traceparent": "00-abc123-def456-01",
	}

	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "test-binding",
				Operation: "create",
				Metadata:  expectedMetadata,
			},
		},
	})
	require.NoError(t, err)

	err = stream.CloseSend()
	require.NoError(t, err)

	resp, err := stream.Recv()
	require.NoError(t, err)

	// Verify metadata was propagated
	assert.Equal(t, expectedMetadata, receivedMetadata)
	assert.Equal(t, expectedMetadata, resp.GetInitialResponse().GetMetadata())
}

func TestInvokeBindingAlpha1_StreamingResponse(t *testing.T) {
	// Test that the handler can stream multiple response chunks
	responseChunks := [][]byte{
		[]byte("response chunk 1"),
		[]byte("response chunk 2"),
		[]byte("response chunk 3"),
	}

	srv := &api{
		logger: logger.NewLogger("test"),
		sendToOutputBindingStreamFn: func(ctx context.Context, stream runtimev1pb.Dapr_InvokeBindingAlpha1Server, name string, req *runtimev1pb.InvokeBindingStreamRequestInitial, data io.Reader) error {
			// Read all input data first
			_, err := io.ReadAll(data)
			if err != nil {
				return err
			}

			// Send initial response
			err = stream.Send(&runtimev1pb.InvokeBindingStreamResponse{
				InvokeBindingStreamResponseType: &runtimev1pb.InvokeBindingStreamResponse_InitialResponse{
					InitialResponse: &runtimev1pb.InvokeBindingStreamResponseInitial{
						Metadata: map[string]string{"status": "ok"},
					},
				},
			})
			if err != nil {
				return err
			}

			// Send multiple response chunks
			for i, chunk := range responseChunks {
				err = stream.Send(&runtimev1pb.InvokeBindingStreamResponse{
					InvokeBindingStreamResponseType: &runtimev1pb.InvokeBindingStreamResponse_Payload{
						Payload: &commonv1pb.StreamPayload{
							Data: chunk,
							Seq:  uint64(i), //nolint:gosec
						},
					},
				})
				if err != nil {
					return err
				}
			}

			return nil
		},
	}
	lis := startTestServerAPI(t, srv)

	clientConn := createTestClient(lis)
	defer clientConn.Close()

	client := runtimev1pb.NewDaprClient(clientConn)
	stream, err := client.InvokeBindingAlpha1(t.Context())
	require.NoError(t, err)

	// Send initial request
	err = stream.Send(&runtimev1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &runtimev1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &runtimev1pb.InvokeBindingStreamRequestInitial{
				Name:      "test-binding",
				Operation: "create",
			},
		},
	})
	require.NoError(t, err)

	err = stream.CloseSend()
	require.NoError(t, err)

	// Collect all responses
	var responses []*runtimev1pb.InvokeBindingStreamResponse
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		responses = append(responses, resp)
	}

	// Should have initial response + 3 payload chunks
	require.Len(t, responses, 4)

	// Verify initial response
	initialResp := responses[0].GetInitialResponse()
	require.NotNil(t, initialResp)
	assert.Equal(t, "ok", initialResp.GetMetadata()["status"])

	// Verify payload chunks
	for i, chunk := range responseChunks {
		payloadResp := responses[i+1].GetPayload()
		require.NotNil(t, payloadResp)
		assert.Equal(t, uint64(i), payloadResp.GetSeq()) //nolint:gosec
		assert.Equal(t, chunk, payloadResp.GetData())
	}
}
