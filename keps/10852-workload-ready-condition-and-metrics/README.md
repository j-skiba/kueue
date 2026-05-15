# KEP-10852: Workload Ready Condition and Queued Workload Metrics

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Standardized Condition Reasons](#standardized-condition-reasons)
    - [Pre-Scheduling Queued States (<code>Ready: False</code>, <code>Admission == nil</code>)](#pre-scheduling-queued-states-ready-false-admission--nil)
    - [Post-Scheduling Gated States (<code>Ready: False</code>, <code>Admission != nil</code>)](#post-scheduling-gated-states-ready-false-admission--nil)
    - [Admission Ready (<code>Ready: True</code>, <code>Admission != nil</code>)](#admission-ready-ready-true-admission--nil)
    - [Terminal States (<code>Ready: False</code>, <code>Admission == nil</code>)](#terminal-states-ready-false-admission--nil)
    - [Reason Tiers and Precedence](#reason-tiers-and-precedence)
- [User Stories](#user-stories)
  - [Story 1](#story-1)
  - [Story 2](#story-2)
  - [Story 3](#story-3)
- [Design Details](#design-details)
  - [Workload Condition](#workload-condition)
  - [Prometheus Metrics](#prometheus-metrics)
    - [Cardinality Safety](#cardinality-safety)
    - [Legacy Metrics Deprecation Plan](#legacy-metrics-deprecation-plan)
  - [Preemption and Eviction Lifecycle Transitions](#preemption-and-eviction-lifecycle-transitions)
    - [The Preemption/Eviction Flow Timeline](#the-preemptioneviction-flow-timeline)
    - [Detailed State &amp; Condition Transitions during Preemption](#detailed-state--condition-transitions-during-preemption)
  - [Coexistence and Decoupling Plan](#coexistence-and-decoupling-plan)
    - [Phase 1: Safe Coexistence (Alpha)](#phase-1-safe-coexistence-alpha)
    - [Phase 2: Decoupling to Physical Field Assertions (Beta/GA)](#phase-2-decoupling-to-physical-field-assertions-betaga)
- [Test Plan](#test-plan)
  - [Unit tests](#unit-tests)
  - [Integration tests](#integration-tests)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP proposes introducing a single, standard **`Ready`** status
condition for Kueue Workloads to completely replace and deprecate both
`Admitted` and `QuotaReserved` conditions. 

The **`Ready`** condition represents the entire progression of a workload
through its lifecycle: from initial submission, through active queueing,
administrative gating, resource quota reservation, admission checks
verification, and final readiness for execution. When a workload is not ready
to run, `Ready` is `False` with high-fidelity, standardized reasons. When it is
fully admitted and ready for pods execution, `Ready` transitions to `True` with
the reason `Admitted`. This unified model eliminates KRM status redundancy and
provides operators with structured Prometheus queued metrics
(`kueue_queued_workloads` and `kueue_local_queue_queued_workloads`) without
causing metric cardinality explosion.

## Motivation

Currently, Kueue's workload state machine has several design and monitoring
challenges:

1. **Redundant and Ambiguous Status Conditions:** A Workload tracks status
   using both `QuotaReserved` (representing that resources are reserved in a
   ClusterQueue) and `Admitted` (representing that all checks are passed and
   pods can start). This split requires programmatic KRM controllers to watch
   multiple conditions and parse complex state transitions.
2. **Asymmetrical and Absent Status Conditions:** The legacy `Admitted`
   condition is completely absent when a workload is initially created and
   queued, leaving newly queued workloads without any status condition
   describing their wait state. Furthermore, the legacy `QuotaReserved` and
   `Admitted` conditions have highly inconsistent presence lifecycles: for
   brand new unattempted workloads, both conditions are completely absent; for
   misconfigured workloads, `QuotaReserved` is present as `False` while
   `Admitted` is absent; and for gated workloads, both are present with
   conflicting statuses. This asymmetrical presence requires programmatic KRM
   consumers to handle complex absence cases and parse inconsistent states.
3. **Overloaded and Ambiguous Reasons:** The legacy `QuotaReserved` condition
   uses the generic `Inadmissible` reason for both permanent user errors
   (e.g., non-existent `LocalQueue`) and administrative holds (e.g., stopped
   queue). Similarly, the `Pending` reason is used for both transient
   resource deficits and fatal flavor mismatches, preventing automated
   alerting and remediation pipelines from distinguishing between them.
4. **Incomplete and Ambiguous Queued Metrics:** Existing Kueue queued metrics
   (`kueue_pending_workloads` and `kueue_local_queue_pending_workloads`)
   group all unadmitted workloads into a single unstructured count.
   Furthermore, workloads pointing to a non-existent `LocalQueue` are
   completely excluded.
5. **API Baggage Constraints:** Modifying the legacy `Admitted` condition to
   enrich wait states is constrained by historic preemption/eviction reason
   strings (`NoReservation`, `NoReservationUnsatisfiedChecks`). Changing these
   reasons risks breaking existing dashboards and external controllers that
   rely on exact string matching.

### Goals

- Standardize Kueue workload observability on a single, unified **`Ready`**
  condition to represent the entire workload lifecycle.
- Provide a strictly bounded set of granular wait reasons under `Ready: False` to
  distinguish between structural blockers, administrative holds, capacity
  deficits, and pending checks.
- Introduce `kueue_queued_workloads` and `kueue_local_queue_queued_workloads`
  Prometheus metrics that count workloads where `Ready` is `False`, mapping
  reasons 1:1.
- Define a safe, multi-phase migration path that guarantees 100% backward
  compatibility during the coexistence phase and decouples internal scheduling
  assertions from condition lifecycles.

### Non-Goals

- Immediately remove or change legacy conditions (both `QuotaReserved` and
  `Admitted` will remain active during early maturity phases to prevent breaking
  downstream dependencies).
- Alter the core scheduling or admission algorithms.
- **Decouple Physical Job/Pod Execution Health**: Tracking physical execution
  health (which is managed by the underlying Job Controller and Kueue's
  `PodsReady` condition) directly within Kueue's `Ready` condition. Kueue's
  `Ready` condition solely represents admission and quota authorization health.
  Terminal workload failures map to `Ready: False (Reason: Finished)` with
  detailed diagnostics in the `Message` field, rather than introducing
  granular execution failure reasons.
- **Track Infrastructure-Level Runtime Failures**: Tracking node-level runtime
  infrastructure events (e.g., node hot-swapping, node failures, hardware
  degradation) directly as distinct reasons in the `Ready` condition. These
  physical failures are handled by Kubernetes' node/pod eviction mechanisms and
  subsequently by the Job Controller, which may trigger Kueue eviction. Kueue
  will only reflect these via the high-level `Evicting` or `Finished` statuses,
  not as native infrastructure failure reasons.

## Proposal

This KEP proposes introducing the **`Ready`** condition in the Workload
`.status.conditions` slice.

### Standardized Condition Reasons

To ensure clear, actionable diagnostics while preventing metric cardinality
explosion, the `Ready` condition supports the following standardized reasons
categorized by lifecycle phase:

#### Pre-Scheduling Queued States (`Ready: False`, `Admission == nil`)

These represent the wait state of a workload before it secures a resource quota
reservation:

* `PendingEvaluation`: The workload has been submitted, is structurally
  valid, resides actively in the queue, and is awaiting its capacity
  evaluation.
* `PendingCapacity`: The workload fits within the maximum limits of the
  ClusterQueue, but the cluster currently lacks sufficient unreserved
  resources.
* `Misconfigured`: A permanent structural error prevents admission (e.g.,
  non-existent `LocalQueue` or `ClusterQueue`, flavor mismatch, DRA
  misconfiguration, or requesting resource quantities exceeding ClusterQueue
  limits). Diagnostic details are populated in the condition's `Message`.
* `Suspended`: The workload is structurally valid, but admission is halting
  due to an administrative hold (e.g., `workload.spec.suspend` is true, or the
  queue's `StopPolicy` is active).
* `WaitingForPodsReady`: Workload admission is held because the scheduler is
  waiting for previously admitted workloads in the queue to reach the
  `PodsReady` condition (under `waitForPodsReady` configuration).

#### Post-Scheduling Gated States (`Ready: False`, `Admission != nil`)

These indicate that the workload holds a quota reservation (locking resources),
but is gated from running or is transitioning:

* `AdmissionGated`: Capacity is reserved, but the workload is waiting on
  downstream admission checks (e.g., VM provisioning via
  `ProvisioningRequest`).
* `PendingTopology`: Capacity is reserved and admission checks are satisfied,
  but the workload is waiting on the Topology-Aware Scheduling (TAS) solver
  to assign physical nodes.
* `Evicting`: The workload is undergoing active eviction (e.g., due to
  preemption, pods ready timeout, or failed admission checks). It still holds
  resource quota while its pods are being terminated, but it is no longer
  ready for execution.

#### Admission Ready (`Ready: True`, `Admission != nil`)

* `Admitted`: All checks and topology placements are fully resolved, and the
  workload is admitted to execute.

#### Terminal States (`Ready: False`, `Admission == nil`)

* `Finished`: The workload has completed its execution lifecycle (either
  successfully or via terminal failure) and its quota reservation has been
  released. It is no longer eligible for scheduling or queueing.

#### Reason Tiers and Precedence

To resolve conflicts when multiple concurrent blocker states apply to a queued
workload, reasons are prioritized into four operational tiers:

- **Tier 0 (Structural Blockers):** `Misconfigured`.
- **Tier 1 (Administrative & Orchestration Holds):** `Suspended`,
  `WaitingForPodsReady`, `AdmissionGated`, `PendingTopology`.
- **Tier 2 (Resource Deficits):** `PendingCapacity`.
- **Tier 3 (Active Queueing):** `PendingEvaluation`.

When multiple causes exist, the lowest tier number (highest severity blocker)
takes absolute precedence:
$$\text{Tier 0} > \text{Tier 1} > \text{Tier 2} > \text{Tier 3}$$
For example, a suspended workload that also lacks quota will report
`Reason: Suspended` (Tier 1) rather than `PendingCapacity` (Tier 2),
highlighting that administrative action is required first.

Terminal states (i.e., when the workload is `Finished`) bypass these
precedence tiers entirely. A finished workload immediately transitions to
`Ready: False (Reason: Finished)` regardless of any pending structural or
resource deficit states, as the workload has reached its terminal lifecycle
stage.

## User Stories

### Story 1
As a Kueue operator, I want to configure automated alerts in Prometheus when my
workloads are stalled due to structural misconfigurations (like a misspelled
LocalQueue or flavor mismatch) so that I can immediately identify and fix
problematic workloads without manual status inspections.

### Story 2
As a platform engineer investigating an active Prometheus alert (e.g.,
`kueue_queued_workloads{reason="Misconfigured"}`), I want a direct 1:1 mapping
to the workload condition reason so I can easily query and locate the exact
workload in the cluster.

### Story 3
As a platform administrator managing both policy engines and Prometheus pipelines,
I want both systems to operate on identical, standardized values (such as
`Misconfigured` or `Suspended`) so I can maintain a unified alerting configuration
across KRM and Prometheus.

## Design Details

### Workload Condition

When the feature gate `WorkloadReadyCondition` is enabled, the workload
controller computes and attaches the `Ready` condition:

```yaml
# When newly created and awaiting scheduling evaluation:
type: Ready
status: "False"
reason: PendingEvaluation
```

```yaml
# When evaluated by the scheduler but capacity is unavailable:
type: Ready
status: "False"
reason: PendingCapacity
```

```yaml
# When quota is reserved but waiting on downstream checks:
type: Ready
status: "False"
reason: AdmissionGated
```

```yaml
# When fully admitted and authorized to run:
type: Ready
status: "True"
reason: Admitted
```

```yaml
# When finished (either successfully or via terminal failure):
type: Ready
status: "False"
reason: Finished
```

### Prometheus Metrics

Two new metrics are introduced:
- `kueue_queued_workloads` (Gauge)
- `kueue_local_queue_queued_workloads` (Gauge)

`kueue_queued_workloads` exposes `cluster_queue` and `reason` labels,
counting queued workloads per ClusterQueue.
`kueue_local_queue_queued_workloads` exposes `name`, `namespace`, and
`reason` labels, counting queued workloads per LocalQueue.
The `reason` label maps 1:1 to the `reason` field of the `Ready` condition
(when `Ready` is `False`). Once the workload transitions to `Ready: True`, it
is automatically excluded from these metrics.

#### Cardinality Safety

The metric series generation is strictly bounded by the number of queues and
reasons:
- At most `len(reasons)` series are generated per active `ClusterQueue` for
  `kueue_queued_workloads`.
- At most `len(reasons)` series are generated per active `LocalQueue` for
  `kueue_local_queue_queued_workloads`.
- The `reason` label only accepts a static set of pre-defined constants
  (currently 8 reasons)

#### Legacy Metrics Deprecation Plan

To prevent metric redundancy and diagnostic confusion, the legacy pending
workloads metrics will be deprecated and eventually removed in favor of the new
queued metrics:

* **Deprecated Metrics:**
  * `kueue_pending_workloads` (replaced by `kueue_queued_workloads`)
  * `kueue_local_queue_pending_workloads` (replaced by
    `kueue_local_queue_queued_workloads`)
* **Migration Timeline:**
  * **Phase 1 (Alpha):** Coexistence. Both legacy and new metrics are active.
    Legacy metrics continue to function exactly as they do today.
  * **Phase 2 (Beta):** Deprecation. Legacy metrics are formally marked as
    deprecated in documentation and release notes.
  * **Phase 3 (GA):** Removal. Legacy metrics are removed from the codebase.

### Preemption and Eviction Lifecycle Transitions

#### The Preemption/Eviction Flow Timeline

```
+---------------+       1. Evict(preemption)       +---------------------+
|   Scheduler   | -------------------------------> | Workload Controller |
+---------------+                                  +---------------------+
                                                              |
                                                              | Sets:
                                                              | - Evicted: True
                                                              | - Ready: False (Evicting)
                                                              v
+---------------+       2. Stop & Cleanup          +---------------------+
| Job Reconciler| -------------------------------> |  Pods / Resources   |
+---------------+                                  +---------------------+
        |                                                     |
        |                                                     | Terminated
        | <---------------------------------------------------+
        v
   3. UnsetAdmission()
        |
        +----------------------------------------> [ Workload Status ]
                                                   - w.Status.Admission: nil
                                                   - Ready: False (PendingEvaluation)
```

#### Detailed State & Condition Transitions during Preemption

1. **Active Running State:**
   - `w.Status.Admission != nil`
   - `Ready` condition: `Status: True, Reason: Admitted`
   - `Evicted` condition: Absent
2. **Eviction Initiated by Scheduler:**
   - The Scheduler calls `workload.Evict` setting `Evicted: True` (Reason:
     `EvictedByPreemption`).
   - `w.Status.Admission` is still non-nil, and `Ready` transitions to
     `Status: False, Reason: Evicting`.
   - *Rationale:* Kueue preserves the `.status.admission` field to represent
     active resource usage until physical cleanup completes, but the `Ready`
     condition immediately signals to the user that the workload is no longer
     executing by transitioning to `False` with `Reason: Evicting`.
3. **Physical Job Suspension:**
   - The Job Reconciler detects `Evicted: True` (or `Ready: False, Reason:
     Evicting`) and deletes the Job's Pods.
4. **Eviction Finalized (Capacity Released):**
   - Once pods are terminated and the job is no longer active, the reconciler
     clears the reservation (`w.Status.Admission = nil`).
   - Triggered by this reset, `UnsetQuotaReservationWithCondition` transitions
     `Ready` to:
     - `Status: False`
     - `Reason: PendingEvaluation` (or `PendingCapacity` if evaluated
       immediately)
     - `Message`: Copy of the preemption/eviction message.
   - *Note:* The legacy `Admitted` condition transitions to `False` here
     (Reason: `NoReservation`), while `Ready` is kept as `False` but
     transitions its reason from `Evicting` to `PendingEvaluation` as it
     returns to the queue.

### Coexistence and Decoupling Plan

To guarantee safe, stable, and deadlock-free operation, a two-phase
migration is proposed:

#### Phase 1: Safe Coexistence (Alpha)

- **Mechanism:** The legacy `QuotaReserved` and `Admitted` conditions are
  kept fully active and untouched in the Workload status. They continue to
  transition exactly as they do today.
- **Rationale:** This guarantees 100% backward compatibility. All existing
  external dashboards, tools, and core scheduler checks (e.g.,
  `HasQuotaReservation` checking `QuotaReserved`) continue to function
  correctly without regression risks.
- **Scope:** The new wait reasons, precedence tiers, and Prometheus queued
  metrics reside exclusively on the newly introduced **`Ready`** condition.
  Legacy metrics (`kueue_pending_workloads` and
  `kueue_local_queue_pending_workloads`) remain fully active and unchanged.

#### Phase 2: Decoupling to Physical Field Assertions (Beta/GA)

- **Mechanism:** Redefine the core `workload.HasQuotaReservation(w)` utility to
  assert directly against the presence of the physical admission field:
  ```go
  func HasQuotaReservation(w *kueue.Workload) bool {
      return w.Status.Admission != nil
  }
  ```
- **Rationale:** Webhook validations and scheduling cycles depend on the
  physical `.status.admission` field. Checking this field directly is cleaner
  than querying status conditions, completely decoupling
  internal scheduler logic from user-facing observability conditions.
- **Deprecation:** Once decoupled, `QuotaReserved` and legacy `Admitted`
  conditions, as well as the legacy pending workloads metrics, are formally
  deprecated and removed in a subsequent release.

## Test Plan

### Unit tests
- `pkg/workload`: Verify correct `Ready` initialization, transition reasons,
  and precedence rules.
- `pkg/workload`: Verify that core scheduling assertions run deadlock-free with
  `HasQuotaReservation` decoupled to `.status.admission` checks.

### Integration tests
- Verify that Prometheus metrics correctly mirror the `Ready` condition status
  and reasons.

## Alternatives

1. **Modifying Legacy `Admitted` Condition:** Rejected because the legacy
   `Admitted` condition carries historic backward-compatibility constraints.
   External dashboards and controllers expect specific preemption reasons
   (`NoReservation`), making it extremely difficult to introduce a clean,
   standardized taxonomy of wait states without breaking existing tools.
2. **Introducing `QuotaAllocated` Condition:** Considered introducing a new
   `QuotaAllocated` condition to replace `QuotaReserved`. While it avoids legacy
   constraints, it results in two similar conditions (`QuotaAllocated` and
   `Admitted`), which leads to user confusion. Because `QuotaReserved` is
   basically a subset of `Admitted` (any admitted workload always holds a
   quota reservation), having both conditions is redundant. Standardizing on
   a single `Ready` condition completely eliminates this redundancy.
3. **Adding a `reason` Label to Existing `kueue_pending_workloads` Metrics:**
   Rejected because legacy reasons (such as overloaded `Inadmissible` and flat
   `Pending`) lack diagnostic granularity, and exporting them would perpetuate
   observability limitations.
4. **Using Kubernetes Events for Inadmissibility Mapping:** Rejected because
   Kubernetes events are transient, not guaranteed to be preserved long-term,
   and decoupled from the `Workload` custom resource state. Utilizing events
   makes it extremely difficult for Kueue to maintain reliable, long-term
   Prometheus gauge metrics for inadmissible workloads without keeping complex
   out-of-band state tracking.
5. **Dedicated `kueue_inadmissible_workloads` Metric:** Rejected because
   introducing separate, specialized metrics increases operational complexity
   and user confusion. Standardizing on a single, unified `Ready` condition
   with highly granular status reasons allows a clean 1:1 mapping to Prometheus
   metrics under the existing `kueue_pending_workloads` namespace without
   fragmenting observability interfaces.
6. **Granular Legacy Reasons:** Rejected because modifying existing legacy
   reason names on legacy conditions (such as `Admitted` or `QuotaReserved`) is
   still a breaking change for existing client integrations that parse or
   expect the older reasons. Introducing a new, standardized `Ready` condition
   allows a clean slate without breaking backward compatibility.

