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

package streaming

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1pb "github.com/dapr/dapr/pkg/proto/common/v1"
	rtv1pb "github.com/dapr/dapr/pkg/proto/runtime/v1"
	"github.com/dapr/dapr/tests/integration/framework"
	"github.com/dapr/dapr/tests/integration/framework/process/binding"
	"github.com/dapr/dapr/tests/integration/framework/process/daprd"
	"github.com/dapr/dapr/tests/integration/framework/socket"
	"github.com/dapr/dapr/tests/integration/suite"
)

func init() {
	suite.Register(new(streaming))
}

type streaming struct {
	daprd   *daprd.Daprd
	binding *binding.OutputBinding
}

func (s *streaming) Setup(t *testing.T) []framework.Option {
	if runtime.GOOS == "windows" {
		t.Skip("skipping unix socket based test on windows")
	}

	sock := socket.New(t)

	s.binding = binding.NewOutputBinding(t,
		binding.WithOutputSocket(sock),
		binding.WithOutputHandler(&binding.EchoOutputBindingHandler{
			ChunkSize: 32 * 1024, // 32KB chunks
		}),
	)

	s.daprd = daprd.New(t,
		daprd.WithResourceFiles(fmt.Sprintf(`
apiVersion: dapr.io/v1alpha1
kind: Component
metadata:
  name: echo-binding
spec:
  type: bindings.%s
  version: v1
`, s.binding.SocketName())),
		daprd.WithSocket(t, sock),
	)

	return []framework.Option{
		framework.WithProcesses(s.binding, s.daprd),
	}
}

func (s *streaming) Run(t *testing.T, ctx context.Context) {
	s.daprd.WaitUntilRunning(t, ctx)

	client := s.daprd.GRPCClient(t, ctx)

	t.Run("small payload single chunk", func(t *testing.T) {
		testStreamingRoundTrip(t, ctx, client, "echo-binding", []byte("hello world"))
	})

	t.Run("medium payload multiple chunks", func(t *testing.T) {
		// 256KB - should result in 8 chunks at 32KB each
		data := make([]byte, 256*1024)
		_, err := rand.Read(data)
		require.NoError(t, err)
		testStreamingRoundTrip(t, ctx, client, "echo-binding", data)
	})

	t.Run("large payload many chunks", func(t *testing.T) {
		// 1MB - should result in 32 chunks at 32KB each
		data := make([]byte, 1024*1024)
		_, err := rand.Read(data)
		require.NoError(t, err)
		testStreamingRoundTrip(t, ctx, client, "echo-binding", data)
	})

	t.Run("empty metadata", func(t *testing.T) {
		stream, err := client.InvokeBindingAlpha1(ctx)
		require.NoError(t, err)

		// Send initial request with no metadata
		err = stream.Send(&rtv1pb.InvokeBindingStreamRequest{
			InvokeBindingStreamRequestType: &rtv1pb.InvokeBindingStreamRequest_InitialRequest{
				InitialRequest: &rtv1pb.InvokeBindingStreamRequestInitial{
					Name:      "echo-binding",
					Operation: "create",
				},
			},
		})
		require.NoError(t, err)

		// Send data
		err = stream.Send(&rtv1pb.InvokeBindingStreamRequest{
			InvokeBindingStreamRequestType: &rtv1pb.InvokeBindingStreamRequest_Payload{
				Payload: &commonv1pb.StreamPayload{
					Data: []byte("test"),
					Seq:  0,
				},
			},
		})
		require.NoError(t, err)

		err = stream.CloseSend()
		require.NoError(t, err)

		// Receive initial response
		resp, err := stream.Recv()
		require.NoError(t, err)
		assert.NotNil(t, resp.GetInitialResponse())

		// Receive data
		var received bytes.Buffer
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			if payload := resp.GetPayload(); payload != nil {
				received.Write(payload.GetData())
			}
		}
		assert.Equal(t, "test", received.String())
	})

	t.Run("with metadata", func(t *testing.T) {
		stream, err := client.InvokeBindingAlpha1(ctx)
		require.NoError(t, err)

		metadata := map[string]string{
			"key1": "value1",
			"key2": "value2",
		}

		// Send initial request with metadata
		err = stream.Send(&rtv1pb.InvokeBindingStreamRequest{
			InvokeBindingStreamRequestType: &rtv1pb.InvokeBindingStreamRequest_InitialRequest{
				InitialRequest: &rtv1pb.InvokeBindingStreamRequestInitial{
					Name:      "echo-binding",
					Operation: "create",
					Metadata:  metadata,
				},
			},
		})
		require.NoError(t, err)

		// Send data
		err = stream.Send(&rtv1pb.InvokeBindingStreamRequest{
			InvokeBindingStreamRequestType: &rtv1pb.InvokeBindingStreamRequest_Payload{
				Payload: &commonv1pb.StreamPayload{
					Data: []byte("test with metadata"),
					Seq:  0,
				},
			},
		})
		require.NoError(t, err)

		err = stream.CloseSend()
		require.NoError(t, err)

		// Receive initial response - should contain metadata
		resp, err := stream.Recv()
		require.NoError(t, err)
		initResp := resp.GetInitialResponse()
		require.NotNil(t, initResp)
		// The echo handler echoes back the metadata
		assert.Equal(t, metadata, initResp.GetMetadata())

		// Receive data
		var received bytes.Buffer
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			if payload := resp.GetPayload(); payload != nil {
				received.Write(payload.GetData())
			}
		}
		assert.Equal(t, "test with metadata", received.String())
	})

	// HTTP Streaming Tests
	httpClient := &http.Client{}

	t.Run("http streaming small payload", func(t *testing.T) {
		testHTTPStreamingRoundTrip(t, ctx, httpClient, s.daprd.HTTPAddress(), "echo-binding", []byte("hello http world"))
	})

	t.Run("http streaming medium payload", func(t *testing.T) {
		// 256KB
		data := make([]byte, 256*1024)
		_, err := rand.Read(data)
		require.NoError(t, err)
		testHTTPStreamingRoundTrip(t, ctx, httpClient, s.daprd.HTTPAddress(), "echo-binding", data)
	})

	t.Run("http streaming large payload", func(t *testing.T) {
		// 1MB
		data := make([]byte, 1024*1024)
		_, err := rand.Read(data)
		require.NoError(t, err)
		testHTTPStreamingRoundTrip(t, ctx, httpClient, s.daprd.HTTPAddress(), "echo-binding", data)
	})

	t.Run("http streaming with metadata", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/v1.0-alpha1/bindings/echo-binding/stream?operation=create", s.daprd.HTTPAddress())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("test with http metadata")))
		require.NoError(t, err)

		// Set metadata headers (using metadata- prefix for request)
		req.Header.Set("metadata-key1", "value1")
		req.Header.Set("metadata-key2", "value2")
		req.Header.Set("Content-Type", "application/octet-stream")

		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Verify response metadata headers (echo binding echoes back metadata)
		// Response uses "metadata." prefix (note the dot, not dash)
		assert.Equal(t, "value1", resp.Header.Get("metadata.key1"))
		assert.Equal(t, "value2", resp.Header.Get("metadata.key2"))

		// Verify response body
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "test with http metadata", string(body))
	})

	t.Run("http streaming with operation header", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/v1.0-alpha1/bindings/echo-binding/stream", s.daprd.HTTPAddress())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("operation via header")))
		require.NoError(t, err)

		// Set operation via header instead of query param
		req.Header.Set("dapr-operation", "create")
		req.Header.Set("Content-Type", "application/octet-stream")

		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "operation via header", string(body))
	})

	t.Run("http streaming missing operation", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/v1.0-alpha1/bindings/echo-binding/stream", s.daprd.HTTPAddress())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("test")))
		require.NoError(t, err)

		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Should fail with bad request
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("http streaming binding not found", func(t *testing.T) {
		url := fmt.Sprintf("http://%s/v1.0-alpha1/bindings/nonexistent/stream?operation=create", s.daprd.HTTPAddress())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("test")))
		require.NoError(t, err)

		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Should fail with internal server error (binding not found)
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	})
}

// testStreamingRoundTrip sends data through the streaming API and verifies
// it comes back correctly.
func testStreamingRoundTrip(t *testing.T, ctx context.Context, client rtv1pb.DaprClient, bindingName string, data []byte) {
	t.Helper()

	stream, err := client.InvokeBindingAlpha1(ctx)
	require.NoError(t, err)

	// Send initial request
	err = stream.Send(&rtv1pb.InvokeBindingStreamRequest{
		InvokeBindingStreamRequestType: &rtv1pb.InvokeBindingStreamRequest_InitialRequest{
			InitialRequest: &rtv1pb.InvokeBindingStreamRequestInitial{
				Name:      bindingName,
				Operation: "create",
			},
		},
	})
	require.NoError(t, err)

	// Send data in chunks (64KB chunks to match typical usage)
	const chunkSize = 64 * 1024
	var seq uint64 = 0
	for offset := 0; offset < len(data); offset += chunkSize {
		end := offset + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]

		err = stream.Send(&rtv1pb.InvokeBindingStreamRequest{
			InvokeBindingStreamRequestType: &rtv1pb.InvokeBindingStreamRequest_Payload{
				Payload: &commonv1pb.StreamPayload{
					Data: chunk,
					Seq:  seq,
				},
			},
		})
		require.NoError(t, err)
		seq++
	}

	err = stream.CloseSend()
	require.NoError(t, err)

	// Receive initial response
	resp, err := stream.Recv()
	require.NoError(t, err)
	assert.NotNil(t, resp.GetInitialResponse(), "expected initial response")

	// Receive all data chunks
	var received bytes.Buffer
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)

		if payload := resp.GetPayload(); payload != nil {
			received.Write(payload.GetData())
		}
	}

	// Verify data integrity
	assert.Equal(t, len(data), received.Len(), "received data length mismatch")
	assert.True(t, bytes.Equal(data, received.Bytes()), "received data content mismatch")
}

// testHTTPStreamingRoundTrip sends data through the HTTP streaming API and verifies
// it comes back correctly.
func testHTTPStreamingRoundTrip(t *testing.T, ctx context.Context, client *http.Client, daprAddr, bindingName string, data []byte) {
	t.Helper()

	url := fmt.Sprintf("http://%s/v1.0-alpha1/bindings/%s/stream?operation=create", daprAddr, bindingName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Read response body
	received, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Verify data integrity
	assert.Equal(t, len(data), len(received), "received data length mismatch")
	assert.True(t, bytes.Equal(data, received), "received data content mismatch")
}
