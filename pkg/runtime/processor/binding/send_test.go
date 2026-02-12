/*
Copyright 2023 The Dapr Authors
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
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/phayes/freeport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dapr/components-contrib/bindings"
	"github.com/dapr/dapr/pkg/api/grpc/manager"
	commonapi "github.com/dapr/dapr/pkg/apis/common"
	componentsV1alpha1 "github.com/dapr/dapr/pkg/apis/components/v1alpha1"
	channelt "github.com/dapr/dapr/pkg/channel/testing"
	bindingsLoader "github.com/dapr/dapr/pkg/components/bindings"
	"github.com/dapr/dapr/pkg/healthz"
	invokev1 "github.com/dapr/dapr/pkg/messaging/v1"
	"github.com/dapr/dapr/pkg/modes"
	runtimev1pb "github.com/dapr/dapr/pkg/proto/runtime/v1"
	"github.com/dapr/dapr/pkg/resiliency"
	"github.com/dapr/dapr/pkg/runtime/channels"
	"github.com/dapr/dapr/pkg/runtime/compstore"
	"github.com/dapr/dapr/pkg/runtime/meta"
	rtmock "github.com/dapr/dapr/pkg/runtime/mock"
	"github.com/dapr/dapr/pkg/runtime/registry"
	"github.com/dapr/dapr/pkg/security"
	daprt "github.com/dapr/dapr/pkg/testing"
	testinggrpc "github.com/dapr/dapr/pkg/testing/grpc"
	"github.com/dapr/kit/crypto/spiffe"
	"github.com/dapr/kit/logger"
)

func TestIsBindingOfExplicitDirection(t *testing.T) {
	t.Run("no direction in metadata input binding", func(t *testing.T) {
		m := map[string]string{}
		r := isBindingOfExplicitDirection("input", m)

		assert.False(t, r)
	})

	t.Run("no direction in metadata output binding", func(t *testing.T) {
		m := map[string]string{}
		r := isBindingOfExplicitDirection("input", m)

		assert.False(t, r)
	})

	t.Run("direction is input binding", func(t *testing.T) {
		m := map[string]string{
			"direction": "input",
		}
		r := isBindingOfExplicitDirection("input", m)

		assert.True(t, r)
	})

	t.Run("direction is output binding", func(t *testing.T) {
		m := map[string]string{
			"direction": "output",
		}
		r := isBindingOfExplicitDirection("output", m)

		assert.True(t, r)
	})

	t.Run("direction is not output binding", func(t *testing.T) {
		m := map[string]string{
			"direction": "input",
		}
		r := isBindingOfExplicitDirection("output", m)

		assert.False(t, r)
	})

	t.Run("direction is not input binding", func(t *testing.T) {
		m := map[string]string{
			"direction": "output",
		}
		r := isBindingOfExplicitDirection("input", m)

		assert.False(t, r)
	})

	t.Run("direction is both input and output binding", func(t *testing.T) {
		m := map[string]string{
			"direction": "output, input",
		}

		r := isBindingOfExplicitDirection("input", m)
		assert.True(t, r)

		r2 := isBindingOfExplicitDirection("output", m)

		assert.True(t, r2)
	})
}

func TestStartReadingFromBindings(t *testing.T) {
	t.Run("OPTIONS request when direction is not specified", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		mockAppChannel.On("InvokeMethod", mock.Anything, mock.Anything).Return(invokev1.NewInvokeMethodResponse(200, "OK", nil), nil)

		m := &rtmock.Binding{}

		b.compStore.AddInputBinding("test", m)
		err := b.StartReadingFromBindings(t.Context())

		require.NoError(t, err)
		assert.True(t, mockAppChannel.AssertCalled(t, "InvokeMethod", mock.Anything, mock.Anything))
	})

	t.Run("No OPTIONS request when direction is specified", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		mockAppChannel.On("InvokeMethod", mock.Anything, mock.Anything).Return(invokev1.NewInvokeMethodResponse(200, "OK", nil), nil)

		m := &rtmock.Binding{
			Metadata: map[string]string{
				"direction": "input",
			},
		}

		b.compStore.AddInputBinding("test", m)
		require.NoError(t, b.compStore.AddPendingComponentForCommit(componentsV1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: componentsV1alpha1.ComponentSpec{
				Type: "bindings.test",
				Metadata: []commonapi.NameValuePair{
					{
						Name: "direction",
						Value: commonapi.DynamicValue{
							JSON: v1.JSON{Raw: []byte("input")},
						},
					},
				},
			},
		}))
		require.NoError(t, b.compStore.CommitPendingComponent())
		err := b.StartReadingFromBindings(t.Context())
		require.NoError(t, err)
		assert.True(t, mockAppChannel.AssertCalled(t, "InvokeMethod", mock.Anything, mock.Anything))
	})
}

func TestGetSubscribedBindingsGRPC(t *testing.T) {
	secP, err := security.New(t.Context(), security.Options{
		TrustAnchors:            []byte("test"),
		AppID:                   "test",
		ControlPlaneTrustDomain: "test.example.com",
		ControlPlaneNamespace:   "default",
		MTLSEnabled:             false,
		OverrideCertRequestFn: func(context.Context, []byte) (*spiffe.SVIDResponse, error) {
			return nil, nil
		},
		Healthz: healthz.New(),
	})
	require.NoError(t, err)
	go secP.Run(t.Context())
	sec, err := secP.Handler(t.Context())
	require.NoError(t, err)

	testCases := []struct {
		name             string
		expectedResponse []string
		responseError    error
		responseFromApp  []string
	}{
		{
			name:             "get list of subscriber bindings success",
			expectedResponse: []string{"binding1", "binding2"},
			responseFromApp:  []string{"binding1", "binding2"},
		},
		{
			name:             "get list of subscriber bindings error from app",
			expectedResponse: []string{},
			responseError:    assert.AnError,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			port, _ := freeport.GetFreePort()
			b := New(Options{
				IsHTTP:         false,
				Resiliency:     resiliency.New(log),
				ComponentStore: compstore.New(),
				Meta:           meta.New(meta.Options{}),
				GRPC:           manager.NewManager(sec, modes.StandaloneMode, &manager.AppChannelConfig{Port: port}),
			})
			// create mock application server first
			grpcServer := testinggrpc.StartTestAppCallbackGRPCServer(t, port, &channelt.MockServer{
				Error:    tc.responseError,
				Bindings: tc.responseFromApp,
			})
			defer grpcServer.Stop()
			// act
			resp, _ := b.getSubscribedBindingsGRPC(t.Context())

			// assert
			assert.Equal(t, tc.expectedResponse, resp, "expected response to match")
		})
	}
}

func TestReadInputBindings(t *testing.T) {
	const testInputBindingName = "inputbinding"
	const testInputBindingMethod = "inputbinding"

	t.Run("app acknowledge, no retry", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		fakeBindingResp := invokev1.NewInvokeMethodResponse(200, "OK", nil)
		defer fakeBindingResp.Close()

		fakeReq := invokev1.NewInvokeMethodRequest(testInputBindingMethod).
			WithHTTPExtension(http.MethodPost, "").
			WithRawDataBytes(rtmock.TestInputBindingData).
			WithContentType("application/json").
			WithMetadata(map[string][]string{})
		defer fakeReq.Close()

		// User App subscribes 1 topics via http app channel
		fakeResp := invokev1.NewInvokeMethodResponse(200, "OK", nil).
			WithRawDataString("OK").
			WithContentType("application/json")
		defer fakeResp.Close()

		mockAppChannel.On("InvokeMethod", mock.MatchedBy(daprt.MatchContextInterface), matchDaprRequestMethod(testInputBindingMethod)).Return(fakeBindingResp, nil)
		mockAppChannel.On("InvokeMethod", mock.MatchedBy(daprt.MatchContextInterface), fakeReq).Return(fakeResp, nil)

		b.compStore.AddInputBindingRoute(testInputBindingName, testInputBindingName)

		mockBinding := rtmock.Binding{}
		ch := make(chan bool, 1)
		mockBinding.ReadErrorCh = ch
		comp := componentsV1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{
				Name: testInputBindingName,
			},
		}
		b.startInputBinding(comp, &mockBinding)

		assert.False(t, <-ch)
	})

	t.Run("app returns error", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		fakeBindingReq := invokev1.NewInvokeMethodRequest(testInputBindingMethod).
			WithHTTPExtension(http.MethodOptions, "").
			WithContentType(invokev1.JSONContentType)
		defer fakeBindingReq.Close()

		fakeBindingResp := invokev1.NewInvokeMethodResponse(200, "OK", nil)
		defer fakeBindingResp.Close()

		fakeReq := invokev1.NewInvokeMethodRequest(testInputBindingMethod).
			WithHTTPExtension(http.MethodPost, "").
			WithRawDataBytes(rtmock.TestInputBindingData).
			WithContentType("application/json").
			WithMetadata(map[string][]string{})
		defer fakeReq.Close()

		// User App subscribes 1 topics via http app channel
		fakeResp := invokev1.NewInvokeMethodResponse(500, "Internal Error", nil).
			WithRawDataString("Internal Error").
			WithContentType("application/json")
		defer fakeResp.Close()

		mockAppChannel.On("InvokeMethod", mock.MatchedBy(daprt.MatchContextInterface), fakeBindingReq).Return(fakeBindingResp, nil)
		mockAppChannel.On("InvokeMethod", mock.MatchedBy(daprt.MatchContextInterface), fakeReq).Return(fakeResp, nil)

		b.compStore.AddInputBindingRoute(testInputBindingName, testInputBindingName)

		mockBinding := rtmock.Binding{}
		ch := make(chan bool, 1)
		mockBinding.ReadErrorCh = ch
		comp := componentsV1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{
				Name: testInputBindingName,
			},
		}
		b.startInputBinding(comp, &mockBinding)

		assert.True(t, <-ch)
	})

	t.Run("binding has data and metadata", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		fakeBindingReq := invokev1.NewInvokeMethodRequest(testInputBindingMethod).
			WithHTTPExtension(http.MethodOptions, "").
			WithContentType(invokev1.JSONContentType)
		defer fakeBindingReq.Close()

		fakeBindingResp := invokev1.NewInvokeMethodResponse(200, "OK", nil)
		defer fakeBindingResp.Close()

		fakeReq := invokev1.NewInvokeMethodRequest(testInputBindingMethod).
			WithHTTPExtension(http.MethodPost, "").
			WithRawDataBytes(rtmock.TestInputBindingData).
			WithContentType("application/json").
			WithMetadata(map[string][]string{"bindings": {"input"}})
		defer fakeReq.Close()

		// User App subscribes 1 topics via http app channel
		fakeResp := invokev1.NewInvokeMethodResponse(200, "OK", nil).
			WithRawDataString("OK").
			WithContentType("application/json")
		defer fakeResp.Close()

		mockAppChannel.On("InvokeMethod", mock.MatchedBy(daprt.MatchContextInterface), matchDaprRequestMethod(testInputBindingMethod)).Return(fakeBindingResp, nil)
		mockAppChannel.On("InvokeMethod", mock.MatchedBy(daprt.MatchContextInterface), fakeReq).Return(fakeResp, nil)

		b.compStore.AddInputBindingRoute(testInputBindingName, testInputBindingName)

		mockBinding := rtmock.Binding{Metadata: map[string]string{"bindings": "input"}}
		ch := make(chan bool, 1)
		mockBinding.ReadErrorCh = ch

		comp := componentsV1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{
				Name: testInputBindingName,
			},
		}
		b.startInputBinding(comp, &mockBinding)

		assert.Equal(t, string(rtmock.TestInputBindingData), mockBinding.Data)
	})

	t.Run("start and stop reading", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		fakeReq := invokev1.NewInvokeMethodRequest("").
			WithHTTPExtension(http.MethodOptions, "").
			WithContentType("application/json")
		defer fakeReq.Close()

		// User App subscribes 1 topics via http app channel
		fakeResp := invokev1.NewInvokeMethodResponse(200, "OK", nil).
			WithContentType("application/json")
		defer fakeResp.Close()

		mockAppChannel.On("InvokeMethod", mock.MatchedBy(daprt.MatchContextInterface), fakeReq).Return(fakeResp, nil)

		closeCh := make(chan struct{})
		defer close(closeCh)

		mockBinding := &daprt.MockBinding{}
		mockBinding.SetOnReadCloseCh(closeCh)
		mockBinding.On("Read", mock.MatchedBy(daprt.MatchContextInterface), mock.Anything).Return(nil).Once()

		comp := componentsV1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{
				Name: testInputBindingName,
			},
		}
		b.compStore.AddInputBinding(testInputBindingName, mockBinding)
		b.startInputBinding(comp, mockBinding)

		time.Sleep(80 * time.Millisecond)

		b.Close(comp)

		select {
		case <-closeCh:
			// All good
		case <-time.After(time.Second):
			t.Fatal("timeout while waiting for binding to stop reading")
		}

		mockBinding.AssertNumberOfCalls(t, "Read", 1)
	})
}

func TestInvokeOutputBindings(t *testing.T) {
	t.Run("output binding missing operation", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		_, err := b.SendToOutputBinding(t.Context(), "mockBinding", &bindings.InvokeRequest{
			Data: []byte(""),
		})
		require.Error(t, err)
		assert.Equal(t, "operation field is missing from request", err.Error())
	})

	t.Run("output binding valid operation", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		b.compStore.AddOutputBinding("mockBinding", &rtmock.Binding{})

		_, err := b.SendToOutputBinding(t.Context(), "mockBinding", &bindings.InvokeRequest{
			Data:      []byte(""),
			Operation: bindings.CreateOperation,
		})
		require.NoError(t, err)
	})

	t.Run("output binding invalid operation", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		b.compStore.AddOutputBinding("mockBinding", &rtmock.Binding{})

		_, err := b.SendToOutputBinding(t.Context(), "mockBinding", &bindings.InvokeRequest{
			Data:      []byte(""),
			Operation: bindings.GetOperation,
		})
		require.Error(t, err)
		assert.Equal(t, "binding mockBinding does not support operation get. supported operations:create list", err.Error())
	})
}

func TestBindingTracingHttp(t *testing.T) {
	b := New(Options{
		IsHTTP:         true,
		Resiliency:     resiliency.New(log),
		ComponentStore: compstore.New(),
		Meta:           meta.New(meta.Options{}),
	})

	t.Run("traceparent passed through with response status code 200", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		mockAppChannel.On("InvokeMethod", mock.Anything, mock.Anything).Return(invokev1.NewInvokeMethodResponse(200, "OK", nil), nil)
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		_, err := b.sendBindingEventToApp(t.Context(), "mockBinding", []byte(""), map[string]string{"traceparent": "00-d97eeaf10b4d00dc6ba794f3a41c5268-09462d216dd14deb-01"})
		require.NoError(t, err)
		mockAppChannel.AssertCalled(t, "InvokeMethod", mock.Anything, mock.Anything)
		assert.Len(t, mockAppChannel.Calls, 1)
		req := mockAppChannel.Calls[0].Arguments.Get(1).(*invokev1.InvokeMethodRequest)
		assert.Contains(t, req.Metadata(), "traceparent")
		assert.Contains(t, req.Metadata()["traceparent"].GetValues(), "00-d97eeaf10b4d00dc6ba794f3a41c5268-09462d216dd14deb-01")
	})

	t.Run("traceparent passed through with response status code 204", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		mockAppChannel.On("InvokeMethod", mock.Anything, mock.Anything).Return(invokev1.NewInvokeMethodResponse(204, "OK", nil), nil)
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		_, err := b.sendBindingEventToApp(t.Context(), "mockBinding", []byte(""), map[string]string{"traceparent": "00-d97eeaf10b4d00dc6ba794f3a41c5268-09462d216dd14deb-01"})
		require.NoError(t, err)
		mockAppChannel.AssertCalled(t, "InvokeMethod", mock.Anything, mock.Anything)
		assert.Len(t, mockAppChannel.Calls, 1)
		req := mockAppChannel.Calls[0].Arguments.Get(1).(*invokev1.InvokeMethodRequest)
		assert.Contains(t, req.Metadata(), "traceparent")
		assert.Contains(t, req.Metadata()["traceparent"].GetValues(), "00-d97eeaf10b4d00dc6ba794f3a41c5268-09462d216dd14deb-01")
	})

	t.Run("bad traceparent does not fail request", func(t *testing.T) {
		mockAppChannel := new(channelt.MockAppChannel)
		mockAppChannel.On("InvokeMethod", mock.Anything, mock.Anything).Return(invokev1.NewInvokeMethodResponse(200, "OK", nil), nil)
		b.channels = new(channels.Channels).WithAppChannel(mockAppChannel)

		_, err := b.sendBindingEventToApp(t.Context(), "mockBinding", []byte(""), map[string]string{"traceparent": "I am not a traceparent"})
		require.NoError(t, err)
		mockAppChannel.AssertCalled(t, "InvokeMethod", mock.Anything, mock.Anything)
		assert.Len(t, mockAppChannel.Calls, 1)
	})
}

func TestBindingResiliency(t *testing.T) {
	b := New(Options{
		Resiliency:     resiliency.FromConfigurations(logger.NewLogger("test"), daprt.TestResiliency),
		Registry:       registry.New(registry.NewOptions()).Bindings(),
		ComponentStore: compstore.New(),
		Meta:           meta.New(meta.Options{}),
	})

	failingChannel := daprt.FailingAppChannel{
		Failure: daprt.NewFailure(
			map[string]int{
				"inputFailingKey": 1,
			},
			map[string]time.Duration{
				"inputTimeoutKey": time.Second * 10,
			},
			map[string]int{},
		),
		KeyFunc: func(req *invokev1.InvokeMethodRequest) string {
			r, _ := io.ReadAll(req.RawData())
			return string(r)
		},
	}

	b.channels = new(channels.Channels).WithAppChannel(&failingChannel)
	b.isHTTP = true

	failingBinding := daprt.FailingBinding{
		Failure: daprt.NewFailure(
			map[string]int{
				"outputFailingKey": 1,
			},
			map[string]time.Duration{
				"outputTimeoutKey": time.Second * 10,
			},
			map[string]int{},
		),
	}

	b.registry.RegisterOutputBinding(
		func(_ logger.Logger) bindings.OutputBinding {
			return &failingBinding
		},
		"failingoutput",
	)

	output := componentsV1alpha1.Component{}
	output.ObjectMeta.Name = "failOutput"
	output.Spec.Type = "bindings.failingoutput"
	err := b.Init(t.Context(), output)
	require.NoError(t, err)

	t.Run("output binding retries on failure with resiliency", func(t *testing.T) {
		req := &bindings.InvokeRequest{
			Data:      []byte("outputFailingKey"),
			Operation: "create",
		}
		_, err := b.SendToOutputBinding(t.Context(), "failOutput", req)

		require.NoError(t, err)
		assert.Equal(t, 2, failingBinding.Failure.CallCount("outputFailingKey"))
	})

	t.Run("output binding times out with resiliency", func(t *testing.T) {
		req := &bindings.InvokeRequest{
			Data:      []byte("outputTimeoutKey"),
			Operation: "create",
		}
		start := time.Now()
		_, err := b.SendToOutputBinding(t.Context(), "failOutput", req)
		end := time.Now()

		require.Error(t, err)
		assert.Equal(t, 2, failingBinding.Failure.CallCount("outputTimeoutKey"))
		assert.Less(t, end.Sub(start), time.Second*10)
	})

	t.Run("input binding retries on failure with resiliency", func(t *testing.T) {
		_, err := b.sendBindingEventToApp(t.Context(), "failingInputBinding", []byte("inputFailingKey"), map[string]string{})

		require.NoError(t, err)
		assert.Equal(t, 2, failingChannel.Failure.CallCount("inputFailingKey"))
	})

	t.Run("input binding times out with resiliency", func(t *testing.T) {
		start := time.Now()
		_, err := b.sendBindingEventToApp(t.Context(), "failingInputBinding", []byte("inputTimeoutKey"), map[string]string{})
		end := time.Now()

		require.Error(t, err)
		assert.Equal(t, 2, failingChannel.Failure.CallCount("inputTimeoutKey"))
		assert.Less(t, end.Sub(start), time.Second*10)
	})
}

func matchDaprRequestMethod(method string) any {
	return mock.MatchedBy(func(req *invokev1.InvokeMethodRequest) bool {
		if req == nil || req.Message() == nil || req.Message().GetMethod() != method {
			return false
		}
		return true
	})
}

// Mock types for streaming tests

// mockStreamInvoker implements bindingsLoader.StreamInvoker for testing.
type mockStreamInvoker struct {
	mock.Mock
	metadata    map[string]string
	contentType string
	recvData    [][]byte
	recvIndex   int
	closed      bool
}

func (m *mockStreamInvoker) Send(ctx context.Context, data []byte) error {
	args := m.Called(ctx, data)
	return args.Error(0)
}

func (m *mockStreamInvoker) Recv(ctx context.Context) ([]byte, error) {
	if m.recvIndex >= len(m.recvData) {
		return nil, io.EOF
	}
	data := m.recvData[m.recvIndex]
	m.recvIndex++
	return data, nil
}

func (m *mockStreamInvoker) GetResponseMetadata() map[string]string {
	return m.metadata
}

func (m *mockStreamInvoker) GetContentType() string {
	return m.contentType
}

func (m *mockStreamInvoker) CloseSend() error {
	args := m.Called()
	return args.Error(0)
}

func (m *mockStreamInvoker) Close() error {
	m.closed = true
	return nil
}

// mockStreamingBinding implements both OutputBinding and StreamingOutputBinding.
type mockStreamingBinding struct {
	mock.Mock
	operations []bindings.OperationKind
	invoker    *mockStreamInvoker
}

func (m *mockStreamingBinding) Init(ctx context.Context, metadata bindings.Metadata) error {
	return nil
}

func (m *mockStreamingBinding) Invoke(ctx context.Context, req *bindings.InvokeRequest) (*bindings.InvokeResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*bindings.InvokeResponse), args.Error(1)
}

func (m *mockStreamingBinding) Operations() []bindings.OperationKind {
	if m.operations == nil {
		return []bindings.OperationKind{bindings.CreateOperation}
	}
	return m.operations
}

func (m *mockStreamingBinding) Close() error {
	return nil
}

func (m *mockStreamingBinding) InvokeStream(ctx context.Context, req *bindingsLoader.StreamInvokeRequest) (bindingsLoader.StreamInvoker, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(bindingsLoader.StreamInvoker), args.Error(1)
}

// mockNonStreamingBinding is a simple binding that doesn't support streaming (used for fallback tests).
type mockNonStreamingBinding struct {
	invokeResp *bindings.InvokeResponse
	invokeErr  error
}

func (m *mockNonStreamingBinding) Init(ctx context.Context, metadata bindings.Metadata) error {
	return nil
}

func (m *mockNonStreamingBinding) Invoke(ctx context.Context, req *bindings.InvokeRequest) (*bindings.InvokeResponse, error) {
	if m.invokeResp == nil {
		return &bindings.InvokeResponse{
			Data:     []byte("response"),
			Metadata: map[string]string{"key": "value"},
		}, m.invokeErr
	}
	return m.invokeResp, m.invokeErr
}

func (m *mockNonStreamingBinding) Operations() []bindings.OperationKind {
	return []bindings.OperationKind{bindings.CreateOperation}
}

func (m *mockNonStreamingBinding) Close() error {
	return nil
}

// mockBindingStream implements runtimev1pb.Dapr_InvokeBindingAlpha1Server for testing.
type mockBindingStream struct {
	mock.Mock
	ctx       context.Context
	sent      []*runtimev1pb.InvokeBindingStreamResponse
	recvQueue []*runtimev1pb.InvokeBindingStreamRequest
	recvIndex int
}

func (m *mockBindingStream) Send(resp *runtimev1pb.InvokeBindingStreamResponse) error {
	m.sent = append(m.sent, resp)
	return nil
}

func (m *mockBindingStream) Recv() (*runtimev1pb.InvokeBindingStreamRequest, error) {
	if m.recvIndex >= len(m.recvQueue) {
		return nil, io.EOF
	}
	req := m.recvQueue[m.recvIndex]
	m.recvIndex++
	return req, nil
}

func (m *mockBindingStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockBindingStream) SendHeader(metadata.MD) error { return nil }
func (m *mockBindingStream) SetTrailer(metadata.MD)       {}
func (m *mockBindingStream) Context() context.Context     { return m.ctx }
func (m *mockBindingStream) SendMsg(msg any) error        { return nil }
func (m *mockBindingStream) RecvMsg(msg any) error        { return nil }

func TestSendToOutputBindingStream(t *testing.T) {
	t.Run("binding not found", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         false,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		stream := &mockBindingStream{ctx: t.Context()}
		initialReq := &runtimev1pb.InvokeBindingStreamRequestInitial{
			Name:      "nonexistent",
			Operation: "create",
		}

		err := b.SendToOutputBindingStream(t.Context(), stream, "nonexistent", initialReq, strings.NewReader("test"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "couldn't find output binding")
	})

	t.Run("missing operation", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         false,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		mockBinding := &rtmock.Binding{}
		b.compStore.AddOutputBinding("testBinding", mockBinding)

		stream := &mockBindingStream{ctx: t.Context()}
		initialReq := &runtimev1pb.InvokeBindingStreamRequestInitial{
			Name:      "testBinding",
			Operation: "",
		}

		err := b.SendToOutputBindingStream(t.Context(), stream, "testBinding", initialReq, strings.NewReader("test"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "operation field is missing")
	})

	t.Run("unsupported operation", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         false,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		mockBinding := &rtmock.Binding{}
		b.compStore.AddOutputBinding("testBinding", mockBinding)

		stream := &mockBindingStream{ctx: t.Context()}
		initialReq := &runtimev1pb.InvokeBindingStreamRequestInitial{
			Name:      "testBinding",
			Operation: "unsupported",
		}

		err := b.SendToOutputBindingStream(t.Context(), stream, "testBinding", initialReq, strings.NewReader("test"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not support operation")
	})

	t.Run("fallback to non-streaming binding", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         false,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		// Use a regular binding that doesn't implement streaming
		mockBinding := &mockNonStreamingBinding{
			invokeResp: &bindings.InvokeResponse{
				Data:     []byte("response data"),
				Metadata: map[string]string{"resp-key": "resp-value"},
			},
		}
		b.compStore.AddOutputBinding("testBinding", mockBinding)

		stream := &mockBindingStream{ctx: t.Context()}
		initialReq := &runtimev1pb.InvokeBindingStreamRequestInitial{
			Name:      "testBinding",
			Operation: "create",
		}

		err := b.SendToOutputBindingStream(t.Context(), stream, "testBinding", initialReq, strings.NewReader("test data"))
		require.NoError(t, err)

		// Should have sent initial response with metadata
		require.GreaterOrEqual(t, len(stream.sent), 1)
		initResp := stream.sent[0].GetInitialResponse()
		require.NotNil(t, initResp)
		assert.Equal(t, map[string]string{"resp-key": "resp-value"}, initResp.GetMetadata())
	})

	t.Run("native streaming binding", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         false,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		// Create mock streaming binding with mock invoker
		invoker := &mockStreamInvoker{
			metadata:    map[string]string{"key": "value"},
			contentType: "application/octet-stream",
			recvData:    [][]byte{[]byte("response data")},
		}
		invoker.On("Send", mock.Anything, mock.Anything).Return(nil)
		invoker.On("CloseSend").Return(nil)

		streamingBinding := &mockStreamingBinding{
			invoker: invoker,
		}
		streamingBinding.On("InvokeStream", mock.Anything, mock.Anything).Return(invoker, nil)

		b.compStore.AddOutputBinding("streamBinding", streamingBinding)

		stream := &mockBindingStream{ctx: t.Context()}
		initialReq := &runtimev1pb.InvokeBindingStreamRequestInitial{
			Name:      "streamBinding",
			Operation: "create",
			Metadata:  map[string]string{"req-key": "req-value"},
		}

		err := b.SendToOutputBindingStream(t.Context(), stream, "streamBinding", initialReq, strings.NewReader("input data"))
		require.NoError(t, err)

		// Should have sent initial response with metadata
		require.GreaterOrEqual(t, len(stream.sent), 1)
		initResp := stream.sent[0].GetInitialResponse()
		require.NotNil(t, initResp)
		assert.Equal(t, map[string]string{"key": "value"}, initResp.GetMetadata())

		// Should have sent data payload
		require.GreaterOrEqual(t, len(stream.sent), 2)
		payload := stream.sent[1].GetPayload()
		require.NotNil(t, payload)
		assert.Equal(t, []byte("response data"), payload.GetData())
	})
}

func TestSendToOutputBindingStreamHTTP(t *testing.T) {
	t.Run("binding not found", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		_, err := b.SendToOutputBindingStreamHTTP(t.Context(), "nonexistent", "create", nil, strings.NewReader("test"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "couldn't find output binding")
	})

	t.Run("missing operation", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		mockBinding := &rtmock.Binding{}
		b.compStore.AddOutputBinding("testBinding", mockBinding)

		_, err := b.SendToOutputBindingStreamHTTP(t.Context(), "testBinding", "", nil, strings.NewReader("test"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "operation field is missing")
	})

	t.Run("unsupported operation", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		mockBinding := &rtmock.Binding{}
		b.compStore.AddOutputBinding("testBinding", mockBinding)

		_, err := b.SendToOutputBindingStreamHTTP(t.Context(), "testBinding", "unsupported", nil, strings.NewReader("test"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not support operation")
	})

	t.Run("fallback to non-streaming binding", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		mockBinding := &mockNonStreamingBinding{
			invokeResp: &bindings.InvokeResponse{
				Data:     []byte("response data"),
				Metadata: map[string]string{"resp-key": "resp-value"},
			},
		}
		b.compStore.AddOutputBinding("testBinding", mockBinding)

		result, err := b.SendToOutputBindingStreamHTTP(t.Context(), "testBinding", "create", map[string]string{"key": "value"}, strings.NewReader("test data"))
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, []byte("response data"), result.Data)
		assert.Equal(t, map[string]string{"resp-key": "resp-value"}, result.Metadata)
	})

	t.Run("native streaming binding with metadata", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         true,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		invoker := &mockStreamInvoker{
			metadata:    map[string]string{"resp-key": "resp-value"},
			contentType: "application/json",
			recvData:    [][]byte{[]byte("response"), []byte(" data")},
		}
		invoker.On("Send", mock.Anything, mock.Anything).Return(nil)
		invoker.On("CloseSend").Return(nil)

		streamingBinding := &mockStreamingBinding{
			invoker: invoker,
		}
		streamingBinding.On("InvokeStream", mock.Anything, mock.Anything).Return(invoker, nil)

		b.compStore.AddOutputBinding("streamBinding", streamingBinding)

		result, err := b.SendToOutputBindingStreamHTTP(t.Context(), "streamBinding", "create", map[string]string{"req-key": "req-value"}, strings.NewReader("input"))
		require.NoError(t, err)
		require.NotNil(t, result)

		assert.Equal(t, []byte("response data"), result.Data)
		assert.Equal(t, map[string]string{"resp-key": "resp-value"}, result.Metadata)
		assert.Equal(t, "application/json", result.ContentType)
	})
}

func TestStreamingBindingMetadataPropagation(t *testing.T) {
	t.Run("metadata flows from binding response to client", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         false,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		expectedMetadata := map[string]string{
			"response-id":   "12345",
			"content-type":  "application/json",
			"custom-header": "custom-value",
		}

		invoker := &mockStreamInvoker{
			metadata:    expectedMetadata,
			contentType: "application/json",
			recvData:    [][]byte{[]byte("data")},
		}
		invoker.On("Send", mock.Anything, mock.Anything).Return(nil)
		invoker.On("CloseSend").Return(nil)

		streamingBinding := &mockStreamingBinding{
			invoker: invoker,
		}
		streamingBinding.On("InvokeStream", mock.Anything, mock.Anything).Return(invoker, nil)

		b.compStore.AddOutputBinding("metadataBinding", streamingBinding)

		stream := &mockBindingStream{ctx: t.Context()}
		initialReq := &runtimev1pb.InvokeBindingStreamRequestInitial{
			Name:      "metadataBinding",
			Operation: "create",
		}

		err := b.SendToOutputBindingStream(t.Context(), stream, "metadataBinding", initialReq, strings.NewReader("input"))
		require.NoError(t, err)

		// Verify metadata in initial response
		require.GreaterOrEqual(t, len(stream.sent), 1)
		initResp := stream.sent[0].GetInitialResponse()
		require.NotNil(t, initResp)
		assert.Equal(t, expectedMetadata, initResp.GetMetadata())
	})
}

func TestStreamingBindingSequenceNumbers(t *testing.T) {
	t.Run("response chunks have sequential sequence numbers", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         false,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		// Multiple data chunks
		invoker := &mockStreamInvoker{
			metadata: map[string]string{},
			recvData: [][]byte{
				[]byte("chunk1"),
				[]byte("chunk2"),
				[]byte("chunk3"),
			},
		}
		invoker.On("Send", mock.Anything, mock.Anything).Return(nil)
		invoker.On("CloseSend").Return(nil)

		streamingBinding := &mockStreamingBinding{
			invoker: invoker,
		}
		streamingBinding.On("InvokeStream", mock.Anything, mock.Anything).Return(invoker, nil)

		b.compStore.AddOutputBinding("seqBinding", streamingBinding)

		stream := &mockBindingStream{ctx: t.Context()}
		initialReq := &runtimev1pb.InvokeBindingStreamRequestInitial{
			Name:      "seqBinding",
			Operation: "create",
		}

		err := b.SendToOutputBindingStream(t.Context(), stream, "seqBinding", initialReq, strings.NewReader("input"))
		require.NoError(t, err)

		// Should have initial response + 3 data chunks
		require.Len(t, stream.sent, 4)

		// First is initial response
		assert.NotNil(t, stream.sent[0].GetInitialResponse())

		// Next 3 are data with sequential sequence numbers
		for i := 1; i < 4; i++ {
			payload := stream.sent[i].GetPayload()
			require.NotNil(t, payload)
			//nolint:gosec // i is always >= 1 so i-1 is always >= 0
			assert.Equal(t, uint64(i-1), payload.GetSeq())
		}
	})
}

func TestStreamingBindingEmptyData(t *testing.T) {
	t.Run("handles empty response data", func(t *testing.T) {
		b := New(Options{
			IsHTTP:         false,
			Resiliency:     resiliency.New(log),
			ComponentStore: compstore.New(),
			Meta:           meta.New(meta.Options{}),
		})

		// Empty data - immediate EOF
		invoker := &mockStreamInvoker{
			metadata: map[string]string{"status": "ok"},
			recvData: [][]byte{}, // No data, just EOF
		}
		invoker.On("Send", mock.Anything, mock.Anything).Return(nil)
		invoker.On("CloseSend").Return(nil)

		streamingBinding := &mockStreamingBinding{
			invoker: invoker,
		}
		streamingBinding.On("InvokeStream", mock.Anything, mock.Anything).Return(invoker, nil)

		b.compStore.AddOutputBinding("emptyBinding", streamingBinding)

		stream := &mockBindingStream{ctx: t.Context()}
		initialReq := &runtimev1pb.InvokeBindingStreamRequestInitial{
			Name:      "emptyBinding",
			Operation: "create",
		}

		err := b.SendToOutputBindingStream(t.Context(), stream, "emptyBinding", initialReq, strings.NewReader(""))
		require.NoError(t, err)

		// Should have just initial response
		require.Len(t, stream.sent, 1)
		initResp := stream.sent[0].GetInitialResponse()
		require.NotNil(t, initResp)
		assert.Equal(t, map[string]string{"status": "ok"}, initResp.GetMetadata())
	})
}
