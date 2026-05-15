# KEP-10852: Workload QuotaAllocated Condition and Queued Workload Metrics

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Standardized High-Level Reasons](#standardized-high-level-reasons)
    - [Precedence Rules](#precedence-rules)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Workload Condition](#workload-condition)
  - [Prometheus Metrics](#prometheus-metrics)
  - [Feature Gate Rollback and Self-Cleaning](#feature-gate-rollback-and-self-cleaning)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable / GA](#stable--ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP introduces a new workload admission condition, `QuotaAllocated`, and a new set
of Prometheus metrics for queued workloads (`kueue_queued_workloads` and
`kueue_local_queue_queued_workloads`). This enhancement categorizes and prioritizes
the various categories of workload admission wait states through a standardized
hierarchy of granular condition reasons, establishes a deterministic precedence
hierarchy of blockers, and provides users with granular observability suitable
for automated alerting without causing metric cardinality explosion.

## Motivation

Currently, Kueue users face several monitoring and observability challenges regarding
workload admission and metrics. This KEP focuses on addressing a few of these key
challenges:

1. **Incomplete and Overloaded Metrics Coverage:** In the existing
   [Kueue metrics](/site/content/en/docs/reference/metrics.md) series
   (`kueue_pending_workloads` and `kueue_local_queue_pending_workloads`, which report
   `status` as `active` or `inadmissible`):
   - Workloads that point to a non-existent or misspelled `LocalQueue` (via
     `kueue.x-k8s.io/local-queue-name`) are omitted entirely from `status=inadmissible`
     counts.
   - **Ambiguous Naming Collision:** The metric label value `status="inadmissible"` shares
     the exact same word as the `QuotaReserved` condition reason `Inadmissible`. However,
     the metric status groups unadmitted workloads into a single unstructured count without
     reporting *why* a workload cannot be admitted or exposing specific condition reasons.
     This naming collision creates user confusion while providing weak diagnostic
     granularity.

2. **Overloaded and Ambiguous Condition Reasons:** While the `QuotaReserved` condition is
   critical for workload admission, its `Reason` field lacks operational granularity in
   several key scenarios:
   - **Overloaded `Inadmissible` Reason:** The `Inadmissible` reason is currently assigned
     both for structural configuration errors (e.g., non-existent queues) and
     administrative holds (e.g., active `StopPolicy`). This lack of granularity prevents
     monitoring systems from differentiating between fatal user errors and intentional
     operational states.
   - **Ambiguity in Flavor Mismatch Reporting:** Workloads that fail to match any
     available `ResourceFlavor` due to node selectors, affinities, or tolerations are
     currently assigned the `Pending` reason for the `QuotaReserved` condition. This
     causes a diagnostic collision with standard quota exhaustion, preventing users and
     automated controllers from distinguishing between configuration incompatibilities and
     transient resource deficits without resorting to unstructured message parsing.
   - **Unification of KRM and Prometheus Alerting Values:** In Kubernetes Resource Model
     (KRM) automation, external controllers can rely on clear condition reasons to assess
     object health and make programmatic decisions. Because current condition reasons are
     either too generic (`Pending`) or overloaded (`Inadmissible`), programmatic KRM
     consumers cannot reliably diagnose stalled workloads. By establishing a 1:1 parity
     between KRM condition reason strings and Prometheus metric `reason` labels, users
     and platform engineers can define robust, unified alerting and automation
     configurations that share identical values across both API inspection and monitoring
     dashboards.

### Goals

- Categorize and prioritize workload admission wait states through a standardized
  hierarchy of granular condition reasons by introducing the `QuotaAllocated`
  condition and granular condition reasons.
- Standardize workload wait states into a strictly bounded set of 6 high-level condition
  reasons (`PendingAdmission`, `PendingCapacity`, `Misconfigured`, `Suspended`,
  `WaitingForPodsReady`, `AdmissionGated`).
- Introduce `kueue_queued_workloads` and `kueue_local_queue_queued_workloads` Prometheus
  metrics featuring extensible `status` and `reason` labels directly mirroring the new
  condition.
- Define robust precedence rules (permanent blockers override temporary ones) and a
  self-cleaning feature gate rollback procedure.

### Non-Goals

- Immediately replace or deprecate `QuotaReserved` (both conditions will coexist during
  early maturity stages to preserve backward compatibility).
- Provide fine-grained error reasons for every specific misconfiguration scenario (e.g.,
  distinguishing between `LocalQueueNotFound` and `ClusterQueueNotFound` in metric
  labels).
- Modify the scheduling or admission core logic itself.

## Proposal

This KEP proposes introducing the `QuotaAllocated` condition to the Workload
`.status.conditions` slice.

### Standardized Condition Reasons

To provide clear observability without causing metric cardinality explosion,
`QuotaAllocated` will utilize a canonical set of reasons categorized into
fundamental wait states and functional admission gates:

- `PendingAdmission`: The workload has been submitted, is structurally valid, is actively
  positioned in the queue, and is simply awaiting its cycle for capacity evaluation.
- `PendingCapacity`: The workload fits within the maximum possible borrowing and nominal
  limits of the ClusterQueue, but the cluster currently lacks sufficient unreserved
  resources. The workload is effectively in the backoff queue awaiting resource
  reclamation or physical cluster autoscaling/node provisioning.
- `Misconfigured`: A permanent structural conflict prevents the workload from ever being
  admitted (e.g., `LocalQueue` or `ClusterQueue` not found, flavor mismatch, Dynamic
  Resource Allocation (DRA) misconfiguration, or requesting resource quantities that
  exceed the maximum possible limits of the ClusterQueue). Detailed error context will be
  provided in the condition's `Message` field.
- `Suspended`: The workload is structurally valid, but admission is intentionally halted by
  an administrative state (e.g., `LocalQueue` or `ClusterQueue` has `StopPolicy` active).
- `WaitingForPodsReady`: Specifically identifies when a workload's admission is
  temporarily held because the scheduler is waiting for previously admitted workloads in
  the queue to reach the `PodsReady` condition (e.g., under `waitForPodsReady`
  configuration). This replaces the ambiguous legacy `Waiting` reason with precise
  operational intent.
- `AdmissionGated`: The workload is gated by an external admission check component.

#### Reason Tiers and Precedence

To ensure future extensibility when new condition reasons are introduced, reasons are
categorized into four distinct operational tiers:

- **Tier 0 (Structural Blockers):** `Misconfigured` (and any future permanent
  configuration impossibility).
- **Tier 1 (Orchestration & Administrative Holds):** `Suspended`, `AdmissionGated`,
  `WaitingForPodsReady`.
- **Tier 2 (Resource Deficits):** `PendingCapacity`.
- **Tier 3 (Active Queueing):** `PendingAdmission`.

When a workload is inadmissible due to multiple concurrent causes across different tiers,
lower tier numbers (higher severity blockers) take strict precedence:
$$\text{Tier 0} > \text{Tier 1} > \text{Tier 2} > \text{Tier 3}$$
When multiple reasons within the exact same tier apply, the controller records the most
recent reason. Detailed diagnostic context will always be accessible in the condition's
`Message` field.

*Multi-Flavor Precedence Example:* Consider a ClusterQueue configured with two flavors:
`on-demand` (which currently has unreserved quota available) and `spot` (which has zero
unreserved quota available). A workload specifies a pod node selector that exclusively
targets `spot` instances. When Kueue evaluates this workload against the ClusterQueue:
- Against `on-demand`, the evaluation yields `Misconfigured` (flavor mismatch against
  unreserved quota).
- Against `spot`, the evaluation yields `PendingCapacity` (valid flavor but quota
  exhausted).

Following the strict precedence rule (`Misconfigured` > `PendingCapacity`),
the workload condition is set to `Misconfigured`. This accurately signals to the user and
platform engineers that although there is unreserved quota available in the ClusterQueue
right now (`on-demand`), this workload cannot be admitted because its configuration
constraints prevent it from utilizing the available capacity.

### User Stories

#### Story 1
As a Kueue user, I want to configure automated alerts in Prometheus when my
workloads are stalled due to structural errors (like a misspelled queue name or
incompatible tolerations) so I don't have to check workloads manually.

#### Story 2
As a Kueue user or cluster administrator investigating an active Prometheus alert or
non-zero metric gauge (e.g., `kueue_queued_workloads{reason="Misconfigured"}`), I want a
direct 1:1 mapping to workload condition reasons so I can instantly correlate and identify
the exact problematic workloads via standard KRM queries without guesswork.

#### Story 3
As a platform administrator managing both programmatic KRM policy engines and Prometheus
monitoring pipelines, I want both systems to operate on identical, standardized values
(such as `Misconfigured` or `Suspended`) so I can maintain robust, unified alerting
configurations without complex value mapping or translation.

### Notes/Constraints/Caveats
- The new condition and metrics will coexist with `QuotaReserved` and
  `kueue_pending_workloads` during alpha and beta.

### Risks and Mitigations
- **Metric Cardinality:** Mitigated by strictly bounding the `reason` label to the 6
  standardized categories rather than unbounded specific error strings.

## Design Details

### Workload Condition

When the feature gate `QuotaAllocatedCondition` is enabled, the workload admission
controller will compute and attach the `QuotaAllocated` condition:

```yaml
Type: QuotaAllocated
Status: True
Reason: Active
```
indicates a workload actively allocated quota.

```yaml
Type: QuotaAllocated
Status: False
Reason: Misconfigured | Suspended | PendingCapacity | WaitingForPodsReady | AdmissionGated
```
indicates an unallocated workload with its precise standardized state.

### Prometheus Metrics

Two new metric series are introduced in the controller namespace:
- `kueue_queued_workloads` (Gauge)
- `kueue_local_queue_queued_workloads` (Gauge)

Both metrics will export the labels: `cluster_queue`, `local_queue`, `status`, and
`reason`. The `reason` label will map 1:1 to the `QuotaAllocated` condition reason.

### Feature Gate Rollback and Self-Cleaning

To safely handle rollback scenarios where an administrator disables
`QuotaAllocatedCondition` after it has been running:
1. The workload controller will revert to evaluating and updating `QuotaReserved`.
2. **Self-Cleaning Reconciler:** During routine workload reconciliation or status updates
   while the feature gate is disabled, the controller will verify whether
   `QuotaAllocated` exists in `.status.conditions`. If present, the controller will
   proactively strip (prune) the stale condition and commit the updated condition slice
   to etcd.
3. Prometheus collectors for the new metrics will cease reporting or zero out gauges when
   the feature gate is disabled.

### Test Plan

[X] I/we understand the owners of the involved components may require updates to existing
tests to make this code solid enough prior to committing the changes necessary to
implement this enhancement.

#### Prerequisite testing updates
- None required beyond standard test framework capabilities.

#### Unit tests
- `pkg/controller/workload`: verify correct `QuotaAllocated` transitions, reason
  assignments, and precedence rules.
- `pkg/controller/workload`: verify self-cleaning rollback logic (pruning
  `QuotaAllocated` when feature gate is off).

#### Integration tests
- `test/integration/controller/workload`: verify that Prometheus metric labels correctly
  mirror `QuotaAllocated` condition updates across workload lifecycles and queue
  deletions.

#### e2e tests
- Sanity E2E test verifying successful workload execution and metrics scraping with
  `QuotaAllocatedCondition` enabled and disabled.

### Graduation Criteria

#### Alpha
- Introduce `QuotaAllocated` condition and new metrics behind `QuotaAllocatedCondition`
  feature gate (disabled by default).
- Gather user feedback on alerting utility and condition usability.

#### Beta
- Enable feature gate by default.
- Ensure robust coexistence with `QuotaReserved`.

#### Stable / GA
- Based on community and user feedback during Beta, determine the final co-existence
  trajectory:
  - **Option A (Primary Path - Replacement):** If user feedback is positive and migration
    is smooth, formally announce the deprecation timeline for `QuotaReserved` and legacy
    `kueue_pending_workloads` metrics, completing the transition to `QuotaAllocated` as
    the primary admission condition.
  - **Option B (Pivot Path - Permanent Co-existence):** If maintaining backward
    compatibility or satisfying distinct downstream consumer needs makes deprecation
    undesirable, officially retain both `QuotaAllocated` and `QuotaReserved` as
    permanent, complementary conditions.

## Implementation History
- 2026-05-15: KEP draft created based on Issue #10852.

## Drawbacks
- Coexistence of two similar conditions (`QuotaAllocated` and `QuotaReserved`) during
  early stages may temporarily cause minor user confusion, mitigated by clear
  documentation.

## Alternatives
1. **Counting Misconfigured Workloads under Existing `status=inadmissible`:** Rejected
   because `inadmissible` would become a massive, unstructured group of unrunnable
   workloads, making root-cause investigation difficult and altering legacy metric
   semantics.
2. **Adding a `reason` Label to Existing `kueue_pending_workloads` Metrics:** Considered
   attaching a `reason` label to the existing pending workload metrics series mapping
   directly to legacy `QuotaReserved` condition reasons. Rejected because legacy reasons in
   `QuotaReserved` (such as overloaded `Inadmissible` and flat `Pending`) are
   insufficient. Exporting them to Prometheus would perpetuate existing ambiguities
   without providing actionable diagnostic granularity.
3. **Adding `DueTo...` Reasons to `QuotaReserved`:** Rejected because changing existing
   reasons from `Pending` to `PendingDueTo...` represents a breaking API change for
   consumers relying on exact string matching.
4. **Separate "Pre-check" Condition (e.g., `Validated`):** Considered adding a separate
   condition to perform static validation (e.g., misspelled queues) before the workload
   enters the scheduler. Rejected because it does not resolve the inherent ambiguity or
   inconsistency within the legacy `QuotaReserved` condition. Crucially, this KEP aims to
   provide a long-term path to formally replace `QuotaReserved`; a separate validation
   condition would merely add complexity while leaving the fundamentally flawed legacy
   source of truth in place.
5. **Using Kubernetes Events:** Rejected because events are ephemeral and do not maintain
   object state suitable for Gauge metric reporting.
