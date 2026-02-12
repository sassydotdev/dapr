# Streaming Output Bindings API

This document describes the streaming output bindings API, which allows applications to send and receive large payloads through output bindings without buffering the entire payload in memory.

## Overview

The streaming API extends the existing output bindings API to support bidirectional streaming. This is useful for:

- Uploading large files to cloud storage (S3, Azure Blob, GCS)
- Streaming data to message queues
- Processing media files
- ETL pipelines with large datasets

## API Status

**Status**: Alpha (`Alpha1` suffix)

This API is experimental and may change in future releases.

## gRPC API

### Method

```protobuf
rpc InvokeBindingAlpha1(stream InvokeBindingStreamRequest)
    returns (stream InvokeBindingStreamResponse) {}
```

### Request Messages

The client sends a stream of `InvokeBindingStreamRequest` messages:

**First message** - Must contain `initial_request`:
```protobuf
message InvokeBindingStreamRequestInitial {
  string name = 1;                    // Binding name (required)
  string operation = 2;               // Operation type (required)
  map<string, string> metadata = 3;   // Optional metadata
}
```

**Subsequent messages** - Contain `payload` with data chunks:
```protobuf
message StreamPayload {
  bytes data = 1;    // Data chunk
  uint64 seq = 2;    // Sequence number (0, 1, 2, ...)
}
```

### Response Messages

The server sends a stream of `InvokeBindingStreamResponse` messages:

**First response** - Contains `initial_response`:
```protobuf
message InvokeBindingStreamResponseInitial {
  map<string, string> metadata = 1;   // Response metadata from binding
}
```

**Subsequent responses** - Contain `payload` with response data chunks:
```protobuf
message StreamPayload {
  bytes data = 1;    // Data chunk
  uint64 seq = 2;    // Sequence number (0, 1, 2, ...)
}
```

### Example (Go)

```go
import (
    rtv1pb "github.com/dapr/dapr/pkg/proto/runtime/v1"
    commonv1pb "github.com/dapr/dapr/pkg/proto/common/v1"
)

// Create stream
stream, err := client.InvokeBindingAlpha1(ctx)
if err != nil {
    return err
}

// Send initial request
err = stream.Send(&rtv1pb.InvokeBindingStreamRequest{
    InvokeBindingStreamRequestType: &rtv1pb.InvokeBindingStreamRequest_InitialRequest{
        InitialRequest: &rtv1pb.InvokeBindingStreamRequestInitial{
            Name:      "my-storage",
            Operation: "create",
            Metadata:  map[string]string{"path": "/uploads/file.bin"},
        },
    },
})

// Send data in chunks
data := []byte("... large data ...")
chunkSize := 64 * 1024 // 64KB
for i := 0; i < len(data); i += chunkSize {
    end := i + chunkSize
    if end > len(data) {
        end = len(data)
    }

    err = stream.Send(&rtv1pb.InvokeBindingStreamRequest{
        InvokeBindingStreamRequestType: &rtv1pb.InvokeBindingStreamRequest_Payload{
            Payload: &commonv1pb.StreamPayload{
                Data: data[i:end],
                Seq:  uint64(i / chunkSize),
            },
        },
    })
}

// Close send side
stream.CloseSend()

// Receive response
for {
    resp, err := stream.Recv()
    if err == io.EOF {
        break
    }
    if err != nil {
        return err
    }

    if initResp := resp.GetInitialResponse(); initResp != nil {
        // Handle metadata
        fmt.Println("Response metadata:", initResp.GetMetadata())
    }

    if payload := resp.GetPayload(); payload != nil {
        // Handle response data
        fmt.Println("Received chunk:", len(payload.GetData()))
    }
}
```

## HTTP API

### Endpoint

```
POST /v1.0-alpha1/bindings/{name}/stream?operation={operation}
```

### Request Headers

| Header | Required | Description |
|--------|----------|-------------|
| `dapr-operation` | Yes* | Operation to perform (alternative to query param) |
| `Content-Type` | No | Content type of request body |
| `metadata-{key}` | No | Metadata to pass to binding (e.g., `metadata-path`) |

*Either `dapr-operation` header or `operation` query parameter is required.

### Request Body

Raw binary data (streamed via chunked transfer encoding for large payloads).

### Response Headers

| Header | Description |
|--------|-------------|
| `Content-Type` | Content type of response body |
| `metadata.{key}` | Response metadata from binding |

### Response Body

Raw binary data (streamed via chunked transfer encoding).

### Example (curl)

```bash
# Upload file using streaming
curl -X POST \
  "http://localhost:3500/v1.0-alpha1/bindings/my-storage/stream?operation=create" \
  -H "Content-Type: application/octet-stream" \
  -H "metadata-path: /uploads/myfile.bin" \
  --data-binary @large-file.bin

# Using operation header
curl -X POST \
  "http://localhost:3500/v1.0-alpha1/bindings/my-storage/stream" \
  -H "dapr-operation: create" \
  -H "Content-Type: application/octet-stream" \
  --data-binary @large-file.bin
```

### Example (Python)

```python
import requests

# Streaming upload
with open('large-file.bin', 'rb') as f:
    response = requests.post(
        'http://localhost:3500/v1.0-alpha1/bindings/my-storage/stream',
        params={'operation': 'create'},
        headers={
            'Content-Type': 'application/octet-stream',
            'metadata-path': '/uploads/myfile.bin',
        },
        data=f,  # Streams the file
        stream=True,  # Stream the response
    )

    # Read response metadata
    for key, value in response.headers.items():
        if key.startswith('metadata.'):
            print(f"Metadata: {key}={value}")

    # Stream response body
    for chunk in response.iter_content(chunk_size=65536):
        process_chunk(chunk)
```

## Error Handling

### gRPC Errors

| Code | Description |
|------|-------------|
| `INVALID_ARGUMENT` | Missing binding name, operation, or invalid sequence |
| `NOT_FOUND` | Binding not found |
| `FAILED_PRECONDITION` | Operation not supported by binding |
| `INTERNAL` | Binding invocation failed |
| `DEADLINE_EXCEEDED` | Timeout waiting for first message (5 seconds) |

### HTTP Errors

| Status | Description |
|--------|-------------|
| 400 | Missing binding name or operation |
| 500 | Binding not found or invocation failed |

## Behavior

### Sequence Numbers

- Request payload sequence numbers must be sequential starting from 0
- Out-of-order sequences cause stream termination with error
- Response sequence numbers are also sequential for ordering verification

### Fallback Behavior

If a binding component doesn't support streaming:
- Dapr buffers the entire request data in memory
- Invokes the standard `Invoke` method
- Streams the response back in chunks

This allows streaming API usage with any binding, with optimal performance for streaming-enabled bindings.

### Resiliency

- Resiliency policies (timeout, circuit breaker, retries) apply to stream establishment
- Once established, the stream proceeds without retry (partial data may have been transmitted)
- Context cancellation properly cleans up resources

### Metrics

The existing `dapr_component_output_binding_count` metric is recorded for streaming invocations with the same dimensions as non-streaming calls.

## Component Implementation

Output binding components can implement streaming by adding the `StreamingOutputBinding` interface:

```go
type StreamingOutputBinding interface {
    bindings.OutputBinding

    InvokeStream(ctx context.Context, req *StreamInvokeRequest) (StreamInvoker, error)
}

type StreamInvoker interface {
    Send(ctx context.Context, data []byte) error
    Recv(ctx context.Context) ([]byte, error)
    GetResponseMetadata() map[string]string
    GetContentType() string
    CloseSend() error
    Close() error
}
```

Pluggable components can implement the `InvokeStream` RPC in the OutputBinding service.

## See Also

- [Decision Record: API-013 Streaming Output Bindings](../decision_records/api/API-013-streaming-output-bindings.md)
- [Output Bindings API](https://docs.dapr.io/reference/api/bindings_api/)
- [Building Blocks: Bindings](https://docs.dapr.io/developing-applications/building-blocks/bindings/)
