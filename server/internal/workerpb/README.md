# `internal/workerpb` — gRPC client to Python AI Worker

Placeholder. The actual `.proto` and generated stubs land in W2 when the worker
team publishes the schema.

## Plan

1. Worker team owns `worker/proto/mem_worker.proto`.
2. Run `make proto` from repo root to generate:
   - `server/internal/workerpb/*.pb.go`
   - `worker/mem_worker/proto/*_pb2.py`
3. memd will dial `MEM_WORKER_GRPC` (e.g. `worker:50051`) and fire-and-forget
   an `IndexFile(file_id)` RPC after each successful upload so the worker can
   pull bytes from S3 and run its Processor pipeline.

## Proposed RPCs (draft, subject to worker team review)

```proto
service Worker {
  rpc IndexFile  (IndexFileRequest)  returns (IndexFileResponse);
  rpc Reindex    (ReindexRequest)    returns (ReindexResponse);
  rpc GetStatus  (GetStatusRequest)  returns (GetStatusResponse);
}

message IndexFileRequest { string file_id = 1; string user_id = 2; }
```

## W1 status

Empty package. memd does NOT attempt to dial the worker yet — `MEM_WORKER_GRPC`
is read into config but unused.
