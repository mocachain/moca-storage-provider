---
title: Uploader
---

# Uploader

Uploader is responsible for receiving and cutting the object payload data into segments, then storing it into PieceStore, and notifying the downstream service to asynchronously replicate the object payload data that processed by EC algorithm to the secondary SP. The workflow of Uploader users can refer [Uploader](../introduction/workflow.md#uploader). We currently abstract SP as the GfSp framework, which provides users with customizable capabilities to meet their specific requirements. Uploader module provides an abstract interface, which is called `Uploader`, as follows:

Uploader is an abstract interface to handle putting object requests from users' account and store their payload data into primary SP piece store.

```go
type Uploader interface {
    Modular
    // PreUploadObject prepares to handle UploadObject, it can do some checks
    // such as checking for duplicates, if limitation of SP has been reached, etc.
    PreUploadObject(ctx context.Context, task task.UploadObjectTask) error
    // HandleUploadObjectTask handles the UploadObject, store payload data into piece store by data stream.
    HandleUploadObjectTask(ctx context.Context, task task.UploadObjectTask, stream io.Reader) error
    // PostUploadObject is called after HandleUploadObjectTask, it can recycle
    // resources, make statistics and do some other operations.
    PostUploadObject(ctx context.Context, task task.UploadObjectTask)
    
    // PreResumableUploadObject prepares to handle ResumableUploadObject, it can do some checks
    // such as checking for duplicates, if limitation of SP has been reached, etc.
    PreResumableUploadObject(ctx context.Context, task task.ResumableUploadObjectTask) error
    // HandleResumableUploadObjectTask handles the ResumableUploadObject, store payload data into piece store by data stream.
    HandleResumableUploadObjectTask(ctx context.Context, task task.ResumableUploadObjectTask, stream io.Reader) error
    // PostResumableUploadObject is called after HandleResumableUploadObjectTask, it can recycle
    // resources, statistics and other operations.
    PostResumableUploadObject(ctx context.Context, task task.ResumableUploadObjectTask)
    
    // QueryTasks queries upload object tasks that running on uploading by task sub-key.
    QueryTasks(ctx context.Context, subKey task.TKey) ([]task.Task, error)
}
```

Uploader interface inherits [Modular interface](./common/lifecycle_modular.md#modular-interface), so Uploader module can be managed by lifecycle and resource manager. In terms of the functions provided by Uploader module, there is only one part: just upload object. It has three methods: PreXXX, HanldeXXX and PostXXX. Therefore, you can rewrite these methods to meet your own requirements. As we can see from the second parameter of the methods defined in `Uploader` interface, uploadObject is split into `UploadObjectTask`. They are also defined as an interface. We can query UploadObject tasks that we care about by `QueryTasks` method through using subKey.

## UploadObjectTask

The corresponding interfaces definition is shown below:

- [ObjectTask](./common/task.md#objecttask)
- [UploadObjectTask](./common/task.md#uploadobjecttask)

ObjectTask inherits [Task interface](./common/task.md#task). UploadObjectTask also defines ten methods to help query info or set data. You can overwrite all these methods in your own.

The corresponding `protobuf` definition is shown below:

- [GfSpUploadObjectTask](./common/proto.md#gfspuploadobjecttask-proto)
- [ObjectInfo](./common/proto.md#objectinfo-proto)
- [Params](./common/proto.md#params-proto)

## ResumableUploadObjectTask

The corresponding interfaces definition is shown below:

- [ObjectTask](./common/task.md#objecttask)
- [ResumableUploadObjectTask](./common/task.md#resumableuploadobjecttask)

ObjectTask inherits [Task interface](./common/task.md#task). UploadObjectTask also defines ten methods to help query info or set data. You can overwrite all these methods in your own.

The corresponding `protobuf` definition is shown below:

- [GfSpResumableUploadObjectTask](./common/proto.md#gfspresumableuploadobjecttask-proto)
- [ObjectInfo](./common/proto.md#objectinfo-proto)
- [Params](./common/proto.md#params-proto)

### Resuming interrupted requests

For objects with a declared payload size, resume offsets must be at a segment boundary at or before persisted progress. Replayed segments must match their stored checksums and are not written or counted again. This lets a final request be retried if its pieces were saved but recording completion failed. Once all declared bytes are stored, a completed request at the payload end with an empty body can also retry finalization. A non-final request that ends partway through a segment returns a closed-stream error without storing that partial segment. Previously completed segments remain available; retry from the last complete segment boundary and resend the discarded bytes. A final request may store a short last segment, but completion requires the total stored size to match the declared payload for both agent and non-agent uploads.

The uploader checks upload state before validating the offset or notifying the manager. Requests for objects already uploaded or being replicated return the existing upload-state conflict error.

An object is reserved for one resumable request from its pre-check until its handler returns. A request for the same object that arrives in between is rejected with a conflict error (code 110010) before the manager is notified or anything is persisted; retry it once the in-flight request has finished. Each manager call made while the object is reserved is limited to 30 seconds; a registration that times out has the manager drop the task and releases the object, and the manager does not keep a registration that completes after the uploader gave up. Once registered, a reservation whose handler never starts expires after 30 seconds: the next request for the object, including a retry at the same offset, has the manager drop the abandoned task before registering its own, and a late handler that finds the object taken is refused. A registration that cannot be dropped stays on record, and the next request for the object retires it before registering its own. Dropped tasks are removed without marking the upload failed.

## GfSp Framework Uploader Code

Uploader module code implementation: [Uploader](https://github.com/mocachain/moca-storage-provider/tree/master/modular/uploader)
