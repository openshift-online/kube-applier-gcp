# kube-applier-gcp

`kube-applier-gcp` is a per-management-cluster controller binary that runs on GKE and brokers
between Google Cloud Firestore and the local Kubernetes apiserver. It reads Desire documents
from Firestore and reconciles them against the cluster.

At a high level:
1. `ApplyDesire` indicates a kube manifest in `.spec.kubeContent` to issue a server-side-apply for.
   Success/failure is written to `.status.conditions["Successful"]`.
2. `DeleteDesire` indicates a kube item in `.spec.targetItem` to issue a delete for.
   Success/failure is written to `.status.conditions["Successful"]`.
3. `ReadDesire` indicates a kube item in `.spec.targetItem` to issue a list/watch+informer for.
   The observed content is written to `.status.kubeContent`.
   Success/failure is written to `.status.conditions["Successful"]`.

## API structure
The API types for this live in `pkg/api/kubeapplier`.

Every `*Desire` API interacts with a single kubernetes resource instance.
We do not support lists, label selection, or list-all.
This is for simplicity in reasoning about the status.

### ManagementCluster
Every `*Desire` API has a `.spec.managementCluster` field.
This is the name of the GKE management cluster that `kube-applier-gcp` is running in.
It matches the value the `kube-applier-gcp` binary was started with via `--management-cluster`.
Each management cluster project has a pair of Firestore named databases
(`specs` and `status`). The database names are fixed within each project.

### Conditions
Each `*Desire` API has a list of conditions.
One of those conditions is the "Successful" condition.
Successful is true if the operation succeeded.
1. For ApplyDesire, this means a successful server-side-apply.
2. For DeleteDesire, this means the item is no longer present in the cluster.
   This is NOT the same as the delete call succeeded, remember that kubernetes has finalizers.
3. For ReadDesire, this means the list/watch succeeded and the informer synced.

When the kube-apiserver call fails,
1. `.status.conditions["Successful"].status` is false
2. `.status.conditions["Successful"].reason` is "KubeAPIError"
3. `.status.conditions["Successful"].message` is the error message from the kube-apiserver call.

When the kube-apiserver call cannot be executed,
1. `.status.conditions["Successful"].status` is false
2. `.status.conditions["Successful"].reason` is "PreCheckFailed"
3. `.status.conditions["Successful"].message` is whatever prevented us from calling the kube-apiserver.

## Status reporting

Every `*Desire` status carries an `ObservedDesireUpdateTime` field set to the
Firestore-managed `UpdateTime` of the spec document that was reconciled. This
tells the backend "I have processed the spec as of this Firestore revision."
There is no application-managed generation counter; the server-managed
`UpdateTime` serves that role.

`ApplyDesireStatus` additionally carries `AppliedResourceGeneration`: the
Kubernetes `.metadata.generation` of the resource after a successful
server-side apply. The backend can use this to confirm that its desired spec
has been applied and the Kubernetes object has advanced to the expected
generation.

## Database structure
Every management cluster has two Firestore **named databases** with IAM-enforced
directional isolation:

| Database | Agent access | Backend access | Contents |
|----------|-------------|----------------|----------|
| `specs` | read-only | read-write | Spec documents written by the backend |
| `status` | read-write | read-only | Status documents written by the agent |

Each database contains three collections with matching document IDs:
```text
database: specs   (agent: read-only)
  applydesires/{uuid}
  deletedesires/{uuid}
  readdesires/{uuid}

database: status  (agent: read-write)
  applydesires/{uuid}
  deletedesires/{uuid}
  readdesires/{uuid}
```

Document IDs are deterministic UUID v5 values generated from
`uuid.NewSHA1(namespaceUUID, "{taskKey}/{group}/{version}/{resource}/{namespace}/{name}")`.
The namespace UUID is a fixed constant shared between the agent and backend
(defined in `pkg/desireid`). The same document ID in both databases links
a spec to its status.

The two-database layout means the agent cannot modify specs and the backend
cannot modify status — IAM enforces this at the database level (Firestore IAM
granularity stops at the database, not the collection). An escape from one
management cluster's pod cannot read or write another cluster's data.

Firestore snapshot listeners provide real-time change notification: the kube-applier
opens a persistent gRPC stream per collection in the **specs database** and receives
document changes as they happen, rather than polling. The informer's resync period
still triggers periodic handler resyncs for cooldown-gated re-reconciliation.

### Authentication and isolation
- GKE Workload Identity Federation: the pod KSA authenticates directly as a WIF principal (no service account keys or intermediary GSA)
- Per-database IAM conditions:
  - `roles/datastore.viewer` on specs-db: `resource.name == "projects/{project}/databases/specs"`
  - `roles/datastore.user` on status-db: `resource.name == "projects/{project}/databases/status"`

### Golang type details for Database
The golang types live in `internal/database`.

`KubeApplierDBClient` is the two-database handle. It carries two open Firestore clients
(specs and status) and exposes read-only accessors for the specs database and full CRUD
accessors for the status database:
- `ApplyDesireSpecs() SpecReader[ApplyDesire]` — read-only, specs-db
- `DeleteDesireSpecs() SpecReader[DeleteDesire]` — read-only, specs-db
- `ReadDesireSpecs() SpecReader[ReadDesire]` — read-only, specs-db
- `ApplyDesireStatus() ResourceCRUD[ApplyDesire]` — read-write, status-db
- `DeleteDesireStatus() ResourceCRUD[DeleteDesire]` — read-write, status-db
- `ReadDesireStatus() ResourceCRUD[ReadDesire]` — read-write, status-db

`SpecReader[T]` provides read-only operations: `Get` and `List`.
`ResourceCRUD[T]` provides flat CRUD operations: `Get`, `List`, `Create`, `Replace`, `Delete`.
`Replace` uses `firestore.Update` with `LastUpdateTime` precondition for optimistic concurrency —
if the document has changed since the last read, the write fails with `codes.FailedPrecondition`
and the status writer treats the conflict as a no-op; a later spec event or informer resync
can reconcile it again. `Replace` uses `Update` (not `Set`) because Firestore's `Set` method
does not accept a `LastUpdateTime` precondition. It replaces `spec`, `status`,
`spec_kubeContent`, and `status_kubeContent`, deleting absent KubeContent fields.

Each desire type carries a `FirestoreMetadata` struct with:
- `DocumentID` — the Firestore document ID (UUID v5)
- `UpdateTime` — server-managed timestamp, used as the optimistic concurrency token
- `CreateTime` — server-managed creation timestamp

These fields use `firestore:"-"` tags and are populated from the `DocumentSnapshot`
server fields, not stored as document data.

`KubeContent` fields (`ApplyDesireSpec.KubeContent`, `ReadDesireStatus.KubeContent`)
also use `firestore:"-"` tags because Firestore's Go SDK codec cannot serialize
`runtime.RawExtension` (it implements `runtime.Object`, which the codec rejects).
The CRUD layer handles these manually: on write, `RawExtension.Raw` is unmarshaled
to `map[string]any` and stored as separate document fields (`spec_kubeContent`,
`status_kubeContent`); on read, the map is marshaled back to JSON bytes.
This means JSON key ordering is not preserved through a round-trip (Firestore maps
are unordered), but the data is semantically identical.

The kube-applier binary opens two databases — specs and status — via
`NewFirestoreKubeApplierDBClient(specsClient, statusClient)`.
The backend service connects to the fixed database names in each management
cluster project; no database-name registry or lister walk is needed.

Controllers receive a `SpecReader` for fetching the current spec from specs-db
and a `ResourceCRUD` for writing status to status-db. The `desirestatuswriter`
package uses a create-or-replace pattern: on first reconcile the status document
does not exist yet (since it lives in a separate database from the spec), so the
writer creates it; on subsequent reconciles it replaces the existing document.

Informers and listers are constructed separately at app wiring time via
`informers.NewKubeApplierInformers(specsClient)`. They watch the **specs database
only** — spec document changes trigger agent reconciliation. The `KubeApplierInformers`
interface returns both a `SharedIndexInformer` (for event handlers) and a typed lister
(for `List()` and `Get(documentID)` lookups) per desire type. Internally, each informer
uses a real Firestore snapshot listener (`collection.Snapshots()`) to stream document
changes into the k8s cache, and a `listWatchWithoutWatchListSemantics` wrapper to opt
out of client-go's WatchList bookmark protocol (which Firestore does not support).

The `internal/database/informers`, `internal/database/listers`, and
`internal/database/listertesting` packages provide the informers and listers for the
`*Desire` APIs.

## Controller structure
The `kube-applier-gcp` binary is controller-based with several controllers.
Consumers should use the `Successful` condition as the operation outcome for
all Desire types. Apply and Delete desires additionally use `Degraded` for
classified controller-level failures. Pre-check and Kubernetes 4xx errors set
`Successful=False` without degradation; other API errors also set
`Degraded=True`. Read desires report only `Successful`.

Change detection uses `UpdateTime`: changed revisions enqueue immediately, while
unchanged informer resync events are cooldown-gated. The field manager for
server-side apply is `gcp-hcp-kube-applier`.

### ReadDesireKubernetesController
An instance of this controller is created and started for each `ReadDesire` instance.
Each instance holds the target reference and spec update time, a single-object
Kubernetes informer, a status-database fetcher/writer, and its work queue. In
addition to informer events, it resyncs every minute so a missing target is also
reported. The sync loop compares the object in the informer store with
`.status.kubeContent` from the status database and writes changes back.

### ReadDesireInformerManagingController
This controller uses the `ReadDesire` informer to feed a sync function for `ReadDesire` instances.
Each time a particular `ReadDesire.spec.targetItem` changes — that is, the
GVR, namespace, or name identifying the kube object to watch (not changes to
the watched object's own content) — the old `ReadDesireKubernetesController`
instance is stopped, discarded, and a new one created.

The manager does not publish a per-launch status condition. The
`ReadDesireKubernetesController` itself owns `Successful` and the
`.status.kubeContent` field, which together carry whether the watch is
working. A separate "watch was last (re)launched at" timestamp turned out
to be uninterpretable — consumers cannot distinguish a target-driven
relaunch from a process restart — so it is not surfaced.

When a `ReadDesire` is deleted, the `ReadDesireKubernetesController` instance is stopped and discarded.

### DeleteDesireController
This controller uses the `DeleteDesire` informer to feed a sync function for `DeleteDesire` instances.
When the sync loop runs, it will:
1. Issue a get for the `.spec.targetItem`
   1. If it doesn't exist, write success and return
   2. If it does exist and has a deletion timestamp, indicate:
      1. `.status.conditions["Successful"].status` is false
      2. `.status.conditions["Successful"].reason` is "WaitingForDeletion"
      3. `.status.conditions["Successful"].message` contains a message that includes the deletion timestamp and UID
      4. and return
   3. If it does exist and has no deletion timestamp:
      1. Issue a delete for the `.spec.targetItem`.
         1. If unsuccessful, use the standard rule for `.status.conditions["Successful"]` and return
         2. If successful, issue another get. If the resource is gone, report
            success; otherwise report `WaitingForDeletion` with its deletion
            timestamp and UID.

This controller resyncs every 60 seconds.

### ApplyDesireController
This controller uses the `ApplyDesire` informer to feed a sync function for `ApplyDesire` instances.
When the sync loop runs, it will:
1. Issue a server-side apply with force the `.spec.kubeContent`
2. Use the standard rules for `.status.conditions["Successful"]`

#### Adopting existing resources
SSA's `force=true` claims field ownership over fields the kube-applier writes
even if a different field manager owned them previously, but it does **not**
delete fields the prior owner wrote that are no longer in our object — those
remain owned by the prior manager. Adopting resources that pre-date the
kube-applier (e.g. created by hand or by another controller) therefore needs a one-time
sweep to clear stale managedFields entries, or careful authoring of the
ApplyDesire's `.spec.kubeContent` to cover every field of interest. We solve
this case-by-case rather than baking adoption logic into the kube-applier.

## Testing
Unit tests use the `internal/database/listertesting` package to create fake Firestore-compatible
database clients with `UpdateTime`-based optimistic concurrency tracking.

Database integration tests use the Firestore emulator configured through
`FIRESTORE_EMULATOR_HOST`. Controller tests use client-go fake Kubernetes clients;
this repository does not currently run a real kube-apiserver in its test suite.
