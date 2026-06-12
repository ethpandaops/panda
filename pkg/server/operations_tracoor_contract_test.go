package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	tracoorgw "github.com/ethpandaops/tracoor/pkg/api"
	tracoorapi "github.com/ethpandaops/tracoor/pkg/proto/tracoor/api"
	gwruntime "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// These contract tests mount Tracoor's own generated grpc-gateway handlers
// (the exact code its production HTTP API runs) over a recording fake server
// and drive panda's tracoor.* operations at it. They prove every route panda
// calls exists and every request body panda builds decodes losslessly into
// Tracoor's proto request types — the gateway's strict protojson decoding
// rejects unknown fields, so silent drift fails loudly when the tracoor
// dependency is bumped.

const (
	tracoorContractBefore = "2026-06-12T00:00:00Z"
	tracoorContractAfter  = "2026-06-01T00:00:00Z"
)

func TestTracoorListArtifactsMatchesGatewayContract(t *testing.T) {
	t.Parallel()

	for artifact, spec := range tracoorArtifacts {
		t.Run(artifact, func(t *testing.T) {
			t.Parallel()

			fake := &tracoorContractServer{}
			svc := newTracoorContractService(t, fake)

			args := tracoorContractFilterArgs(artifact, spec)
			args["id"] = "11111111-2222-3333-4444-555555555555"
			args["limit"] = 7
			args["offset"] = 3
			args["order_by"] = "fetched_at ASC"

			rec := httptest.NewRecorder()
			handled := svc.handleTracoorOperation("tracoor.list_artifacts", rec, newTracoorOpRequest(t, args))

			require.True(t, handled)
			require.Equal(t, http.StatusOK, rec.Code, "gateway rejected panda's request: %s", rec.Body.String())
			require.NotNil(t, fake.last, "the fake Tracoor server never received the request")

			msg := fake.last.ProtoReflect()
			assertTracoorContractFilters(t, msg, spec)
			assert.Equal(t, "11111111-2222-3333-4444-555555555555", tracoorProtoString(t, msg, "id"))

			pagination := tracoorProtoMessage(t, msg, "pagination")
			assert.Equal(t, int64(7), pagination.Get(pagination.Descriptor().Fields().ByName("limit")).Int())
			assert.Equal(t, int64(3), pagination.Get(pagination.Descriptor().Fields().ByName("offset")).Int())
			assert.Equal(t, "fetched_at ASC", pagination.Get(pagination.Descriptor().Fields().ByName("order_by")).String())
		})
	}
}

func TestTracoorCountArtifactsMatchesGatewayContract(t *testing.T) {
	t.Parallel()

	for artifact, spec := range tracoorArtifacts {
		t.Run(artifact, func(t *testing.T) {
			t.Parallel()

			fake := &tracoorContractServer{}
			svc := newTracoorContractService(t, fake)

			rec := httptest.NewRecorder()
			handled := svc.handleTracoorOperation(
				"tracoor.count_artifacts", rec, newTracoorOpRequest(t, tracoorContractFilterArgs(artifact, spec)))

			require.True(t, handled)
			require.Equal(t, http.StatusOK, rec.Code, "gateway rejected panda's request: %s", rec.Body.String())
			require.NotNil(t, fake.last, "the fake Tracoor server never received the request")

			assertTracoorContractFilters(t, fake.last.ProtoReflect(), spec)
		})
	}
}

func TestTracoorListUniqueValuesMatchesGatewayContract(t *testing.T) {
	t.Parallel()

	for artifact, spec := range tracoorArtifacts {
		t.Run(artifact, func(t *testing.T) {
			t.Parallel()

			fake := &tracoorContractServer{}
			svc := newTracoorContractService(t, fake)

			fields := make([]any, 0, len(spec.uniqueFields))
			for _, field := range spec.uniqueFields {
				fields = append(fields, field)
			}

			rec := httptest.NewRecorder()
			handled := svc.handleTracoorOperation("tracoor.list_unique_values", rec, newTracoorOpRequest(t, map[string]any{
				"network":  "testnet",
				"artifact": artifact,
				"fields":   fields,
			}))

			require.True(t, handled)
			require.Equal(t, http.StatusOK, rec.Code, "gateway rejected panda's request: %s", rec.Body.String())
			require.NotNil(t, fake.last, "the fake Tracoor server never received the request")

			// Every field name panda declares for the artifact must round-trip
			// into Tracoor's per-type Field enum.
			msg := fake.last.ProtoReflect()
			fd := msg.Descriptor().Fields().ByName("fields")
			require.NotNil(t, fd)

			received := make([]string, 0, len(spec.uniqueFields))

			list := msg.Get(fd).List()
			for i := 0; i < list.Len(); i++ {
				enumValue := fd.Enum().Values().ByNumber(list.Get(i).Enum())
				require.NotNil(t, enumValue)
				received = append(received, string(enumValue.Name()))
			}

			assert.Equal(t, spec.uniqueFields, received)
		})
	}
}

func TestTracoorGetConfigMatchesGatewayContract(t *testing.T) {
	t.Parallel()

	fake := &tracoorContractServer{}
	svc := newTracoorContractService(t, fake)

	rec := httptest.NewRecorder()
	handled := svc.handleTracoorOperation("tracoor.get_config", rec, newTracoorOpRequest(t, map[string]any{
		"network": "testnet",
	}))

	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code, "gateway rejected panda's request: %s", rec.Body.String())
	require.IsType(t, &tracoorapi.GetConfigRequest{}, fake.last)
}

// tracoorContractFilterArgs builds operation args exercising every filter
// the artifact spec declares.
func tracoorContractFilterArgs(artifact string, spec tracoorArtifact) map[string]any {
	args := map[string]any{"network": "testnet", "artifact": artifact}

	for _, key := range spec.stringFilters {
		switch key {
		case "before":
			args[key] = tracoorContractBefore
		case "after":
			args[key] = tracoorContractAfter
		default:
			args[key] = "value-" + key
		}
	}

	for i, key := range spec.numberFilters {
		args[key] = 1000 + i
	}

	return args
}

// assertTracoorContractFilters verifies every filter panda sent landed in
// the decoded proto request with its exact value.
func assertTracoorContractFilters(t *testing.T, msg protoreflect.Message, spec tracoorArtifact) {
	t.Helper()

	for _, key := range spec.stringFilters {
		switch key {
		case "before", "after":
			expected, err := time.Parse(time.RFC3339, map[string]string{
				"before": tracoorContractBefore,
				"after":  tracoorContractAfter,
			}[key])
			require.NoError(t, err)

			timestamp, ok := tracoorProtoMessage(t, msg, key).Interface().(*timestamppb.Timestamp)
			require.True(t, ok)
			assert.Equal(t, expected.Unix(), timestamp.GetSeconds(), "field %s", key)
		default:
			assert.Equal(t, "value-"+key, tracoorProtoString(t, msg, key), "field %s", key)
		}
	}

	for i, key := range spec.numberFilters {
		fd := msg.Descriptor().Fields().ByName(protoreflect.Name(key))
		require.NotNil(t, fd, "field %s missing from Tracoor request type", key)

		// Tracoor mixes integer shapes: slot/epoch are plain uint64,
		// block_number is int64, and index is a UInt64Value wrapper.
		switch fd.Kind() {
		case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
			protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
			assert.Equal(t, int64(1000+i), msg.Get(fd).Int(), "field %s", key)
		case protoreflect.MessageKind:
			wrapper := msg.Get(fd).Message()
			value := wrapper.Get(wrapper.Descriptor().Fields().ByName("value"))
			assert.Equal(t, uint64(1000+i), value.Uint(), "field %s", key)
		default:
			assert.Equal(t, uint64(1000+i), msg.Get(fd).Uint(), "field %s", key)
		}
	}
}

func tracoorProtoString(t *testing.T, msg protoreflect.Message, name string) string {
	t.Helper()

	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "field %s missing from Tracoor request type", name)

	return msg.Get(fd).String()
}

func tracoorProtoMessage(t *testing.T, msg protoreflect.Message, name string) protoreflect.Message {
	t.Helper()

	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "field %s missing from Tracoor request type", name)

	return msg.Get(fd).Message()
}

// newTracoorContractService starts Tracoor's generated gateway over the fake
// server and returns a panda service whose testnet Tracoor URL points at it.
func newTracoorContractService(t *testing.T, fake *tracoorContractServer) *service {
	t.Helper()

	mux := gwruntime.NewServeMux()
	require.NoError(t, tracoorgw.RegisterAPIHandlerServer(context.Background(), mux, fake))

	gateway := httptest.NewServer(mux)
	t.Cleanup(gateway.Close)

	log := logrus.New()
	log.SetOutput(io.Discard)

	return &service{
		log:        log,
		httpClient: gateway.Client(),
		cartographoorClient: tracoorOperationCartographoor{
			networks: map[string]discovery.Network{
				"testnet": {
					Name:        "testnet",
					Status:      "active",
					ServiceURLs: &discovery.ServiceURLs{Tracoor: gateway.URL},
				},
			},
		},
	}
}

// tracoorContractServer records the last decoded request for assertions.
type tracoorContractServer struct {
	tracoorapi.UnimplementedAPIServer

	last proto.Message
}

func (s *tracoorContractServer) GetConfig(_ context.Context, req *tracoorapi.GetConfigRequest) (*tracoorapi.GetConfigResponse, error) {
	s.last = req
	return &tracoorapi.GetConfigResponse{}, nil
}

func (s *tracoorContractServer) ListBeaconState(_ context.Context, req *tracoorapi.ListBeaconStateRequest) (*tracoorapi.ListBeaconStateResponse, error) {
	s.last = req
	return &tracoorapi.ListBeaconStateResponse{}, nil
}

func (s *tracoorContractServer) CountBeaconState(_ context.Context, req *tracoorapi.CountBeaconStateRequest) (*tracoorapi.CountBeaconStateResponse, error) {
	s.last = req
	return &tracoorapi.CountBeaconStateResponse{}, nil
}

func (s *tracoorContractServer) ListUniqueBeaconStateValues(_ context.Context, req *tracoorapi.ListUniqueBeaconStateValuesRequest) (*tracoorapi.ListUniqueBeaconStateValuesResponse, error) {
	s.last = req
	return &tracoorapi.ListUniqueBeaconStateValuesResponse{}, nil
}

func (s *tracoorContractServer) ListBeaconBlock(_ context.Context, req *tracoorapi.ListBeaconBlockRequest) (*tracoorapi.ListBeaconBlockResponse, error) {
	s.last = req
	return &tracoorapi.ListBeaconBlockResponse{}, nil
}

func (s *tracoorContractServer) CountBeaconBlock(_ context.Context, req *tracoorapi.CountBeaconBlockRequest) (*tracoorapi.CountBeaconBlockResponse, error) {
	s.last = req
	return &tracoorapi.CountBeaconBlockResponse{}, nil
}

func (s *tracoorContractServer) ListUniqueBeaconBlockValues(_ context.Context, req *tracoorapi.ListUniqueBeaconBlockValuesRequest) (*tracoorapi.ListUniqueBeaconBlockValuesResponse, error) {
	s.last = req
	return &tracoorapi.ListUniqueBeaconBlockValuesResponse{}, nil
}

func (s *tracoorContractServer) ListBeaconBadBlock(_ context.Context, req *tracoorapi.ListBeaconBadBlockRequest) (*tracoorapi.ListBeaconBadBlockResponse, error) {
	s.last = req
	return &tracoorapi.ListBeaconBadBlockResponse{}, nil
}

func (s *tracoorContractServer) CountBeaconBadBlock(_ context.Context, req *tracoorapi.CountBeaconBadBlockRequest) (*tracoorapi.CountBeaconBadBlockResponse, error) {
	s.last = req
	return &tracoorapi.CountBeaconBadBlockResponse{}, nil
}

func (s *tracoorContractServer) ListUniqueBeaconBadBlockValues(_ context.Context, req *tracoorapi.ListUniqueBeaconBadBlockValuesRequest) (*tracoorapi.ListUniqueBeaconBadBlockValuesResponse, error) {
	s.last = req
	return &tracoorapi.ListUniqueBeaconBadBlockValuesResponse{}, nil
}

func (s *tracoorContractServer) ListBeaconBadBlob(_ context.Context, req *tracoorapi.ListBeaconBadBlobRequest) (*tracoorapi.ListBeaconBadBlobResponse, error) {
	s.last = req
	return &tracoorapi.ListBeaconBadBlobResponse{}, nil
}

func (s *tracoorContractServer) CountBeaconBadBlob(_ context.Context, req *tracoorapi.CountBeaconBadBlobRequest) (*tracoorapi.CountBeaconBadBlobResponse, error) {
	s.last = req
	return &tracoorapi.CountBeaconBadBlobResponse{}, nil
}

func (s *tracoorContractServer) ListUniqueBeaconBadBlobValues(_ context.Context, req *tracoorapi.ListUniqueBeaconBadBlobValuesRequest) (*tracoorapi.ListUniqueBeaconBadBlobValuesResponse, error) {
	s.last = req
	return &tracoorapi.ListUniqueBeaconBadBlobValuesResponse{}, nil
}

func (s *tracoorContractServer) ListExecutionBlockTrace(_ context.Context, req *tracoorapi.ListExecutionBlockTraceRequest) (*tracoorapi.ListExecutionBlockTraceResponse, error) {
	s.last = req
	return &tracoorapi.ListExecutionBlockTraceResponse{}, nil
}

func (s *tracoorContractServer) CountExecutionBlockTrace(_ context.Context, req *tracoorapi.CountExecutionBlockTraceRequest) (*tracoorapi.CountExecutionBlockTraceResponse, error) {
	s.last = req
	return &tracoorapi.CountExecutionBlockTraceResponse{}, nil
}

func (s *tracoorContractServer) ListUniqueExecutionBlockTraceValues(_ context.Context, req *tracoorapi.ListUniqueExecutionBlockTraceValuesRequest) (*tracoorapi.ListUniqueExecutionBlockTraceValuesResponse, error) {
	s.last = req
	return &tracoorapi.ListUniqueExecutionBlockTraceValuesResponse{}, nil
}

func (s *tracoorContractServer) ListExecutionBadBlock(_ context.Context, req *tracoorapi.ListExecutionBadBlockRequest) (*tracoorapi.ListExecutionBadBlockResponse, error) {
	s.last = req
	return &tracoorapi.ListExecutionBadBlockResponse{}, nil
}

func (s *tracoorContractServer) CountExecutionBadBlock(_ context.Context, req *tracoorapi.CountExecutionBadBlockRequest) (*tracoorapi.CountExecutionBadBlockResponse, error) {
	s.last = req
	return &tracoorapi.CountExecutionBadBlockResponse{}, nil
}

func (s *tracoorContractServer) ListUniqueExecutionBadBlockValues(_ context.Context, req *tracoorapi.ListUniqueExecutionBadBlockValuesRequest) (*tracoorapi.ListUniqueExecutionBadBlockValuesResponse, error) {
	s.last = req
	return &tracoorapi.ListUniqueExecutionBadBlockValuesResponse{}, nil
}
