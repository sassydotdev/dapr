# API-013: Streaming Output Bindings

## Status
Proposed

## Context

Output bindings in Dapr currently require the entire payload to be buffered in memory before being sent to the target system. This creates several limitations:

1. **Memory constraints**: Large payloads (e.g., multi-GB files) cannot be practically handled as they exceed available memory
2. **Latency**: The entire payload must be received before processing can begin (no streaming/pipelining)
3. **Timeout issues**: Large uploads may exceed configured timeouts before any data is transmitted
4. **Resource inefficiency**: Memory is held for the entire duration of the transfer

Many output binding targets naturally support streaming:
- Cloud storage (S3, Azure Blob, GCS) - support streaming uploads
- Message queues - can stream large messages in chunks
- HTTP endpoints - support chunked transfer encoding
- File systems - naturally support streaming writes

### Use Cases

1. **Large file uploads**: Upload multi-GB files to cloud storage without buffering
2. **Real-time data pipelines**: Stream sensor data or logs to storage as they arrive
3. **Video/media processing**: Stream media files to processing services
4. **Backup/archival**: Stream database dumps or backups to storage
5. **ETL pipelines**: Stream transformed data to destination systems

### Prior Art

Dapr already supports streaming in other APIs:
- **Configuration streaming** (`SubscribeConfiguration`) - streams configuration changes
- **Pub/Sub streaming** (`SubscribeTopicEventsAlpha1`) - streams topic events
- **Conversation API** - streams LLM responses

This proposal follows established patterns from these existing streaming APIs.

## Decisions

### API Design

#### gRPC Streaming API

A new bidirectional streaming RPC is added to the Dapr service:

```protobuf
// In dapr.proto
rpc InvokeBindingAlpha1(stream InvokeBindingStreamRequest)
    returns (stream InvokeBindingStreamResponse) {}
```

The `Alpha1` suffix indicates this is an experimental API that may change before stabilization.

#### Request Message Structure

```protobuf
message InvokeBindingStreamRequest {
  oneof invoke_binding_stream_request_type {
    InvokeBindingStreamRequestInitial initial_request = 1;
    common.v1.StreamPayload payload = 2;
  }
}

message InvokeBindingStreamRequestInitial {
  string name = 1;           // Binding name (required)
  string operation = 2;      // Operation type (required)
  map<string, string> metadata = 3;  // Optional metadata
}
```

- First message MUST contain `initial_request` with binding name and operation
- Subsequent messages contain `payload` with data chunks and sequence numbers
- Sequence numbers enable ordering validation and future retry capabilities

#### Response Message Structure

```protobuf
message InvokeBindingStreamResponse {
  oneof invoke_binding_stream_response_type {
    InvokeBindingStreamResponseInitial initial_response = 1;
    common.v1.StreamPayload payload = 2;
  }
}

message InvokeBindingStreamResponseInitial {
  map<string, string> metadata = 1;  // Response metadata from binding
}
```

- First response contains `initial_response` with binding metadata
- Subsequent responses contain `payload` with response data chunks
- This enables bidirectional streaming where both request and response can be large

#### HTTP Streaming API

A new HTTP endpoint supports streaming via chunked transfer encoding:

```
POST /v1.0-alpha1/bindings/{name}/stream?operation={operation}
Content-Type: application/octet-stream
Transfer-Encoding: chunked

[streaming request body]
```

Response:
```
HTTP/1.1 200 OK
Content-Type: application/octet-stream
Transfer-Encoding: chunked
X-Dapr-Metadata-*: [response metadata headers]

[streaming response body]
```

### Component Interface

Output bindings that support streaming implement an optional interface:

```go
type StreamingOutputBinding interface {
    OutputBinding

    // InvokeStream initiates a streaming invocation
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

Bindings that don't implement this interface automatically fall back to buffered invocation.

### Pluggable Component Protocol

The pluggable component protocol is extended with streaming support:

```protobuf
// In components/v1/bindings.proto
service OutputBinding {
  // Existing
  rpc Init(OutputBindingInitRequest) returns (OutputBindingInitResponse) {}
  rpc Invoke(InvokeRequest) returns (InvokeResponse) {}
  rpc ListOperations(ListOperationsRequest) returns (ListOperationsResponse) {}
  rpc Ping(PingRequest) returns (PingResponse) {}

  // New streaming method
  rpc InvokeStream(stream InvokeStreamRequest) returns (stream InvokeStreamResponse) {}
}
```

### Backwards Compatibility

- The streaming API is additive - existing non-streaming bindings continue to work
- When streaming is used with a non-streaming binding, Dapr buffers the data and invokes the standard `Invoke` method
- The `Alpha1` suffix allows API changes before stabilization
- No changes to existing binding component interfaces are required

### Error Handling

- Invalid sequence numbers return an error and close the stream
- Context cancellation properly cleans up resources
- Binding errors are propagated back through the stream
- Timeout on initial request (5 seconds) prevents resource leaks from abandoned streams

### Resiliency

- Resiliency policies apply to stream establishment (the initial handshake)
- Once a stream is established, retries are not attempted (partial data may have been transmitted)
- Circuit breakers can prevent new stream creation when a binding is unhealthy

### Metrics

The existing `dapr_component_output_binding_count` metric tracks streaming invocations with the same dimensions as non-streaming calls.

## Consequences

### Positive

1. **Large payload support**: Files of any size can be processed with constant memory usage
2. **Reduced latency**: Data can be processed as it arrives (pipelining)
3. **Better resource utilization**: No large memory allocations for payload buffering
4. **Consistent with existing patterns**: Follows established Dapr streaming conventions
5. **Backwards compatible**: Existing bindings and applications continue to work

### Negative

1. **Increased complexity**: Streaming code is more complex than request/response
2. **Debugging difficulty**: Streaming issues can be harder to diagnose
3. **SDK updates required**: SDKs need updates to expose streaming APIs

### Neutral

1. **Component updates optional**: Binding components can add streaming support incrementally
2. **Fallback behavior**: Non-streaming bindings work with streaming API (with buffering)

## Implementation

### Phase 1 (Current)
- [x] gRPC streaming API implementation
- [x] HTTP streaming endpoint
- [x] Pluggable component streaming protocol
- [x] Fallback to buffered invocation
- [x] Unit tests
- [x] Integration tests

### Phase 2 (Future)
- [ ] SDK support (Go, Python, .NET, Java, JavaScript)
- [ ] Built-in binding streaming implementations (S3, Azure Blob, GCS)
- [ ] Documentation on docs.dapr.io
- [ ] Performance benchmarks
- [ ] Promote to stable API (remove Alpha1 suffix)

## References

- [Dapr Bindings Spec](https://docs.dapr.io/reference/api/bindings_api/)
- [gRPC Bidirectional Streaming](https://grpc.io/docs/what-is-grpc/core-concepts/#bidirectional-streaming-rpc)
- [HTTP Chunked Transfer Encoding](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Transfer-Encoding)
