#!/usr/bin/env python3
# Parse Galera PodRemediator E2E timeline log (e2e-galera-wsrep-monitor output) and emit a
# Disaster Recovery & Availability markdown report for the last complete iteration.
# Inserts "Reading the Kubernetes events (plain language)" heuristic summaries before raw event excerpts.
# Usage: galera_e2e_dr_report.py <timeline_log_path> [output_md_path]
# If output_md_path is omitted, writes to stdout.
from __future__ import annotations

import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

BASELINE_MARKER = "# Stage: after pod/PV colocation check — before virsh destroy"
NOTREADY_MARKER = "# Stage: node NotReady (after virsh destroy)"
NHC_MARKER = "# Stage: after NHC listing (unhealthy node visible to remediation stack)"
PVC_POLL_MARKER = "# Stage: start PVC deletion / volume-change poll"
PVC_REM_MARKER = "# Stage: PVC remediated (deleted or new volumeName)"
POST_RESCHED_MARKER = "# Stage: StatefulSet test pod Running (post-reschedule)"
FINAL_MARKER = "# Stage: target worker node Ready after virsh start"

STAGE_HEADER = re.compile(r"^# Stage:\s*(.+)$")
TS_LINE = re.compile(r"^# Timestamp:\s*(.+)$")
WSREP_LINE = re.compile(r"^\|\s*(openstack-galera-\d+)\s*\|")
WSREP_TS = re.compile(r"=== \[([^\]]+)\]\s*(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})\+00:00\s*===")
PVC_PV = re.compile(
    r"mysql-db-openstack-galera-(\d)\s+.*Successfully provisioned volume\s+(pvc-[a-f0-9-]+)",
    re.IGNORECASE,
)

# Lines in a stage block that are worth quoting in the markdown (Galera + remediation story).
_K8S_EVIDENCE_RX = re.compile(
    r"(?i)(openstack-galera|mysql-db-openstack-galera|galera\.openstack|podremediator|"
    r"selfnoderemediation|self-node-remediation|nodehealthcheck|noderemediation|machinehealthcheck|"
    r"killpod|failedkillpod|volumedelete|volumeattachment|failedattach|failedmount|"
    r"nodeNotReady|not\s*ready|remediation\.openstack|mariadb-operator|"
    r"persistentvolumeclaim/mysql-db-openstack-galera|"
    r"provisioning.*galera|externallydeleted|waitforfirstconsumer)",
)

_WSREP_SECTION_START = re.compile(r"^=== Galera", re.MULTILINE)
_K8S_SECTION_START = re.compile(r"^=== (?:Kubernetes events|K8s Events)", re.MULTILINE)


def _parse_ts_line(line: str) -> datetime | None:
    m = TS_LINE.match(line.strip())
    if not m:
        return None
    raw = m.group(1).strip()
    try:
        if raw.endswith("Z"):
            return datetime.fromisoformat(raw[:-1] + "+00:00")
        return datetime.fromisoformat(raw)
    except ValueError:
        return None


def _parse_wsrep_ts(block: str) -> str | None:
    m = WSREP_TS.search(block)
    return m.group(2) if m else None


def _parse_table_rows(stage_block: str) -> list[dict[str, Any]]:
    """Rows from --- wsrep summary --- markdown table (compact log format)."""
    rows: list[dict[str, Any]] = []
    in_table = False
    new_layout = False
    for line in stage_block.splitlines():
        if "--- wsrep summary" in line:
            in_table = True
            new_layout = False
            continue
        if in_table and line.startswith("--- wsrep detail"):
            break
        if not in_table:
            continue
        if line.strip().startswith("|") and "Pod |" in line and "Ready |" in line:
            new_layout = "wsrep_last_committed" in line
            continue
        if not line.strip().startswith("|") or "---" in line:
            continue
        m = WSREP_LINE.match(line)
        if not m:
            continue
        parts = [p.strip() for p in line.split("|")]
        parts = [p for p in parts if p]
        if new_layout:
            if len(parts) < 8:
                continue
            rows.append(
                {
                    "pod": parts[0],
                    "ready": parts[1],
                    "phase": parts[2],
                    "node": parts[3],
                    "last_committed": parts[4],
                    "cluster_size": parts[5],
                    "cluster_status": parts[6],
                    "local_state": parts[7],
                }
            )
        else:
            if len(parts) < 7:
                continue
            rows.append(
                {
                    "pod": parts[0],
                    "ready": parts[1],
                    "phase": parts[2],
                    "node": parts[3],
                    "last_committed": "—",
                    "cluster_size": parts[4],
                    "cluster_status": parts[5],
                    "local_state": parts[6],
                }
            )
    return rows


def _extract_stage(full: str, marker: str) -> tuple[str | None, str | None]:
    """Return (stage_title, block from this # Stage: through the next # Stage: or EOF)."""
    idx = full.find(marker)
    if idx < 0:
        return None, None
    rest = full[idx:]
    title_line = rest.split("\n", 1)[0]
    title = title_line.replace("# Stage:", "").strip()
    # Do not cut at inner ################################################################################ (banner repeats); only next stage header.
    next_stage = rest.find("\n# Stage:", max(len(marker), 8))
    if next_stage < 0:
        block = rest
    else:
        block = rest[:next_stage]
    return title, block


def _last_iteration_slice(text: str) -> str | None:
    """Last timeline segment from baseline through final Ready (best effort)."""
    starts = [m.start() for m in re.finditer(re.escape(BASELINE_MARKER), text)]
    if not starts:
        return None
    last_start = starts[-1]
    tail = text[last_start:]
    if FINAL_MARKER not in tail:
        if len(starts) >= 2:
            prev = starts[-2]
            tail = text[prev:]
            last_start = prev
    if FINAL_MARKER not in tail:
        return None
    # End after FINAL_MARKER section (include trailing events until next baseline or EOF)
    fi = tail.find(FINAL_MARKER)
    rest_after = tail[fi:]
    nb = rest_after.find(BASELINE_MARKER, len(FINAL_MARKER))
    if nb > 0:
        return tail[: fi + nb]
    return tail


def _pvc_map_for_slice(slice_text: str) -> dict[str, list[str]]:
    """Ordinal -> ordered list of PV names seen in ProvisioningSucceeded lines."""
    out: dict[str, list[str]] = {}
    for m in PVC_PV.finditer(slice_text):
        ord_ = m.group(1)
        pv = m.group(2)
        out.setdefault(ord_, []).append(pv)
    return out


def _fmt_table(rows: list[dict[str, Any]], pvc_map: dict[str, list[str]]) -> str:
    lines = [
        "| Pod | Ready | Node | wsrep_last_committed | wsrep_cluster_size | wsrep_cluster_status | wsrep_local_state | Storage (PVC → PV hint) |",
        "|-----|-------|------|---------------------|--------------------|----------------------|-------------------|-------------------------|",
    ]
    for r in rows:
        pod = r["pod"]
        ord_ = pod.rsplit("-", 1)[-1]
        pvs = pvc_map.get(ord_, [])
        pv_hint = f"`mysql-db-openstack-galera-{ord_}` → `{pvs[-1]}`" if pvs else "*(see events in log)*"
        lc = r.get("last_committed", "—")
        lines.append(
            f"| {pod} | {r['ready']} | {r['node']} | {lc} | {r['cluster_size']} | "
            f"{r['cluster_status']} | {r['local_state']} | {pv_hint} |"
        )
    return "\n".join(lines)


def _last_committed_leader_note(rows: list[dict[str, Any]]) -> str:
    """Single-line hint: which row had max wsrep_last_committed (Galera is not single-master)."""
    best: tuple[int, str, str] | None = None
    for r in rows:
        raw = str(r.get("last_committed", "")).strip()
        if not raw.isdigit():
            continue
        val = int(raw)
        pod = str(r.get("pod", ""))
        node = str(r.get("node", ""))
        if best is None or val > best[0] or (val == best[0] and pod < best[1]):
            best = (val, pod, node)
    if not best:
        return ""
    val, pod, node = best
    return (
        f"> **Highest `wsrep_last_committed` in this table:** `{val}` on **`{pod}`** (node `{node}`). "
        "Galera has no dedicated primary; this is the member with the most applied cert-bounded writes among "
        "reachable pods shown — same ordering as `e2e_galera_victim_selection=max_wsrep_last_committed` at victim pick."
    )


def _table_md(rows: list[dict[str, Any]], pvc_map: dict[str, list[str]]) -> str:
    body = _fmt_table(rows, pvc_map)
    note = _last_committed_leader_note(rows)
    return body + ("\n\n" + note if note else "")


def _duration_str(t0: datetime | None, t1: datetime | None) -> str:
    if not t0 or not t1:
        return "*(timestamps not parsed)*"
    delta = t1 - t0
    sec = int(delta.total_seconds())
    if sec < 0:
        return "*(invalid order)*"
    m, s = divmod(sec, 60)
    h, m = divmod(m, 60)
    if h:
        return f"{h}h {m}m {s}s (~{sec}s)"
    return f"{m}m {s}s (~{sec}s)"


def _stage_log_timestamp(block: str) -> str | None:
    for line in block.splitlines()[:12]:
        m = TS_LINE.match(line.strip())
        if m:
            return m.group(1).strip()
    return None


def _extract_wsrep_excerpt(block: str, max_lines: int = 32) -> str:
    """First chunk of the Galera/wsrep block (STS line, summary table, key probe lines)."""
    m = _WSREP_SECTION_START.search(block)
    if not m:
        return ""
    start = m.start()
    km = _K8S_SECTION_START.search(block, start)
    chunk = block[start : km.start()] if km else block[start:]
    lines = [ln.rstrip() for ln in chunk.splitlines() if ln.strip()]
    if len(lines) > max_lines:
        lines = lines[:max_lines] + [f"... ({len(chunk.splitlines()) - max_lines} more lines in log)"]
    return "\n".join(lines)


def _k8s_scan_tail(block: str) -> str:
    """Only the timeline tail where `oc get events` was appended (skip wsrep / mysql blocks)."""
    m = _K8S_SECTION_START.search(block)
    if not m:
        return ""
    return block[m.start() :]


_GALERA_MAIN_RX = re.compile(
    r"(?i)(pod/openstack-galera-\d|persistentvolumeclaim/mysql-db-openstack-galera-\d\b)",
)


def _k8s_evidence_priority(line: str) -> int:
    """Lower = show first. Tier 0 = main openstack-galera STS / its PVCs; 1 = remediation; 2 = other."""
    low = line.lower()
    if _GALERA_MAIN_RX.search(line) or ("openstack-galera" in low and "cell" not in low):
        return 0
    if any(
        x in low
        for x in (
            "podremediator",
            "selfnoderemediation",
            "self-node-remediation",
            "nodehealthcheck",
            "noderemediation",
            "killpod",
            "failedkillpod",
            "volumedelete",
            "failedattach",
            "failedmount",
        )
    ):
        return 1
    if "mariadb-operator" in low or "infra-operator" in low:
        return 3
    return 2


def _summarize_k8s_events(scan: str, stage: str) -> str:
    """Plain-language bullets derived from the raw `oc get events` tail for this stage."""
    scan = scan.strip()
    if not scan:
        return ""

    lines = [ln for ln in scan.splitlines() if ln.strip()]
    joined = "\n".join(lines)

    def any_line(pat: str) -> bool:
        return any(re.search(pat, ln, re.I) for ln in lines)

    intros = {
        "baseline": (
            "**What the Kubernetes events are telling you** (steady state before fault):"
        ),
        "notready": (
            "**What the Kubernetes events are telling you** (right after worker loss / node NotReady):"
        ),
        "nhc": "**What the Kubernetes events are suggesting** (remediation stack visible):",
        "poll": "**What the Kubernetes events are suggesting** (PVC still changing):",
        "pvc_rem": "**What the Kubernetes events are suggesting** (PVC remediated):",
        "post_resched": "**What the Kubernetes events are suggesting** (pod rescheduled):",
        "final": "**What the Kubernetes events are suggesting** (worker back / node Ready):",
    }
    intro = intros.get(stage, "**What the Kubernetes events are suggesting:**")

    bullets: list[str] = []

    if stage == "baseline":
        bullets.append(
            "- **Event window:** `oc get events` is a **rolling tail** (newest and older lines mixed). "
            "The **age** in the first column (`17s` vs `9m`) shows how fresh a line is—do not treat a **9m** "
            "**NodeNotReady** as the current story unless it matches the stage time. For Galera, trust the "
            "**wsrep** excerpt first."
        )

    # Stale NotReady/Taint lines often appear in the same tail as the spread-lab snapshot; skip for baseline.
    if stage != "baseline":
        if any_line(r"NodeNotReady|node is not ready"):
            bullets.append(
                "- **Worker NotReady:** one or more pods report **Node is not ready** — the kubelet on that "
                "worker stopped posting heartbeats (matches a powered-off or isolated node after `virsh destroy`). "
                "Pods that stayed on that node are unreachable until the node recovers or workloads reschedule."
            )
        if any_line(r"TaintManagerEviction"):
            bullets.append(
                "- **Evictions:** **TaintManagerEviction** means the control plane is clearing pods from an "
                "unschedulable/unhealthy node so replacements can run elsewhere."
            )
    if any_line(r"openstack-galera-\d") and any_line(r"Unhealthy|Readiness probe failed"):
        if re.search(r"Donor/Desynced|desync", joined, re.I):
            bullets.append(
                "- **Galera readiness:** **Unhealthy** / readiness failures mentioning **Donor/Desynced** usually "
                "mean a member is **wsrep_desync** or catching up — common during the spread lab or after topology "
                "changes; compare with the **wsrep** excerpt above."
            )
        else:
            bullets.append(
                "- **Galera probes:** readiness failures on **openstack-galera-*** pods mean MariaDB/Galera did not "
                "pass the probe yet (cluster still forming, SST, or networking)."
            )
    if any_line(r"mysql-db-openstack-galera|persistentvolumeclaim/.*galera"):
        if any_line(r"ProvisioningSucceeded|ExternallyProvisioned|provisioned volume"):
            bullets.append(
                "- **PVC / PV:** **ProvisioningSucceeded** (or similar) for **`mysql-db-openstack-galera-*`** means "
                "a **new local volume** was bound — this is the happy path after PodRemediator cleared a stale claim "
                "on the failed node."
            )
        if any_line(r"ExternalProvisioning|Waiting for a volume"):
            bullets.append(
                "- **Volume wait:** **ExternalProvisioning** / “waiting for a volume” — the storage provisioner "
                "is still creating the LV/PV for the claim."
            )
    if any_line(r"PodRemediator|podremediator"):
        bullets.append(
            "- **PodRemediator:** events referencing **PodRemediator** show the operator reconciling namespaces "
            "for PVC remediation."
        )
    if any_line(r"SelfNodeRemediation|Self-Node-Remediation|selfnoderemediation"):
        bullets.append(
            "- **SNR:** **SelfNodeRemediation** events relate to the automated node remediation flow (often paired "
            "with Metal3 / unhealthy-machine signals)."
        )
    if any_line(r"KillPod|FailedKillPod"):
        bullets.append(
            "- **Forced pod deletion:** **KillPod** / **FailedKillPod** — remediation tried to delete workload pods "
            "stuck on the bad node so StatefulSets can recreate them."
        )
    if any_line(r"FailedAttachVolume|FailedMount|FailedAttach"):
        bullets.append(
            "- **Storage attach:** **FailedAttach** / **FailedMount** — the PV exists but the kubelet could not attach "
            "or mount it yet (timing, node transition, or CSI)."
        )
    if any_line(r"VolumeAttachment|volumeattachment"):
        bullets.append(
            "- **VolumeAttachment:** controller activity attaching/detaching volumes to nodes."
        )
    if any_line(r"Scheduled\s+pod/openstack-galera"):
        bullets.append(
            "- **Scheduling:** an **openstack-galera** pod was **Scheduled** to a worker — follow-up to PVC bind "
            "and StatefulSet rollout."
        )
    # Omit routine leader-election churn from baseline (floods the summary).
    if stage != "baseline" and any_line(r"LeaderElection|became leader"):
        bullets.append(
            "- **Operators:** **LeaderElection** lines are normal controller churn (who holds the lease); "
            "they often spike during node transitions."
        )

    if not bullets:
        bullets.append(
            "- No strong automated pattern matched this tail beyond noise filtering—use the **verbatim excerpt** "
            "below if you need pod names, or rely on the **wsrep** block as ground truth for Galera."
        )

    out = [intro, ""]
    # Cap length for readability
    out.extend(bullets[:12])
    if len(bullets) > 12:
        out.append(f"- *(…{len(bullets) - 12} more heuristic bullet(s) omitted.)*")
    return "\n".join(out) + "\n"


def _extract_k8s_evidence_lines(block: str, limit: int = 26) -> list[str]:
    """Lines from the K8s events section(s) that match remediation/Galera relevance (Galera-first ordering)."""
    scan = _k8s_scan_tail(block)
    if not scan:
        return []
    scored: list[tuple[int, int, str]] = []
    seen: set[str] = set()
    for i, raw in enumerate(scan.splitlines()):
        line = raw.rstrip()
        if not line.strip() or not _K8S_EVIDENCE_RX.search(line):
            continue
        key = line.strip()
        if key in seen:
            continue
        seen.add(key)
        scored.append((_k8s_evidence_priority(line), i, line))
    scored.sort(key=lambda t: (t[0], t[1]))
    return [t[2] for t in scored[:limit]]


def _md_fence(label: str, body: str, lang: str = "text") -> str:
    body = body.strip()
    if not body:
        return f"_{label}: (nothing to show in this slice)_\n"
    body = body.replace("\r\n", "\n")
    return f"**{label}**\n\n```{lang}\n{body}\n```\n"


def _colocation_note(rows: list[dict[str, Any]]) -> str:
    by_node: dict[str, list[str]] = {}
    for r in rows:
        if r["node"] in ("", "—", "?"):
            continue
        by_node.setdefault(r["node"], []).append(r["pod"])
    dup = {n: ps for n, ps in by_node.items() if len(ps) > 1}
    if not dup:
        return "No Galera pod colocation on a single worker in this snapshot."
    parts = [f"**{n}**: {', '.join(ps)}" for n, ps in sorted(dup.items())]
    return "Pod colocation (higher blast radius on single worker failure): " + "; ".join(parts)


def _quorum_note(rows: list[dict[str, Any]]) -> str:
    reachable = sum(1 for r in rows if r.get("cluster_size") not in (None, "", "—") and str(r["cluster_size"]).isdigit())
    sizes = [int(r["cluster_size"]) for r in rows if str(r.get("cluster_size", "")).isdigit()]
    mx = max(sizes) if sizes else 0
    if mx < 2:
        return "**SERVICE OUTAGE / QUORUM RISK:** fewer than 2 members visible in wsrep on reachable pods, or mysql unreachable on all ordinals."
    if mx == 2:
        return "**DEGRADED:** cluster size 2 — split-brain / quorum risk until third member rejoins."
    return f"wsrep reports up to **{mx}** members on reachable pods (check per-pod mysql reachability in log)."


def _emit_step_section(
    lines_out: list[str],
    *,
    title: str,
    e2e_what: str,
    block: str,
    rows: list[dict[str, Any]],
    pvc_map: dict[str, list[str]],
    summary_note: str | None,
    include_table: bool = True,
    k8s_stage: str = "baseline",
) -> None:
    """One numbered step: narrative, timestamp, log excerpts, optional interpretation, table."""
    lines_out.append(title + "\n")
    lines_out.append("### What the E2E did at this milestone\n\n")
    lines_out.append(e2e_what.strip() + "\n\n")
    ts = _stage_log_timestamp(block)
    if ts:
        lines_out.append(f"**Recorded in timeline:** `{ts}`\n\n")
    ws = _extract_wsrep_excerpt(block)
    lines_out.append(_md_fence("Log excerpt — Galera / wsrep (from timeline)", ws))
    lines_out.append("\n")
    k8s_tail = _k8s_scan_tail(block)
    narr = _summarize_k8s_events(k8s_tail, k8s_stage)
    if narr.strip():
        lines_out.append("### Reading the Kubernetes events (plain language)\n\n")
        lines_out.append(narr + "\n")
    k8s_lines = _extract_k8s_evidence_lines(block)
    if k8s_lines:
        lines_out.append(
            _md_fence(
                "Log excerpt — Kubernetes events (Galera / PVC / remediation keywords only)",
                "\n".join(k8s_lines),
            )
        )
        if not any(_GALERA_MAIN_RX.search(x) for x in k8s_lines):
            lines_out.append(
                "> **Note:** This tail does not include a direct `pod/openstack-galera-*` or "
                "`mysql-db-openstack-galera-*` line (the E2E monitor caps lines and regex-filters noise). "
                "The **wsrep excerpt** and **parsed table** above are the ground truth for Galera in this stage.\n\n"
            )
    else:
        lines_out.append(
            "**Log excerpt — Kubernetes events**\n\n"
            "*(No lines in this stage matched the Galera/remediation keyword filter; "
            "the playbook still captured a full `oc get events` window in the raw timeline file.)*\n\n"
        )
    if summary_note:
        lines_out.append("### Interpretation\n\n")
        lines_out.append(summary_note.strip() + "\n\n")
    if include_table and rows:
        lines_out.append("### wsrep summary table (parsed)\n\n")
        lines_out.append(_table_md(rows, pvc_map) + "\n\n")
    elif include_table:
        lines_out.append("### wsrep summary table\n\n*(No summary table rows parsed in this slice.)*\n\n")


def generate_report(log_text: str) -> str:
    it = _last_iteration_slice(log_text)
    if not it:
        return "# Galera E2E DR report\n\n*(Could not find a complete iteration: baseline + final node Ready in log.)*\n"

    pvc_map = _pvc_map_for_slice(it)

    def stage_block(marker: str) -> str:
        _, b = _extract_stage(it, marker)
        return b or ""

    b0 = stage_block(BASELINE_MARKER)
    b1 = stage_block(NOTREADY_MARKER)
    b_nhc = stage_block(NHC_MARKER)
    b_poll = stage_block(PVC_POLL_MARKER)
    b_pvc = stage_block(PVC_REM_MARKER)
    b_post = stage_block(POST_RESCHED_MARKER)
    b_final = stage_block(FINAL_MARKER)

    rows_baseline = _parse_table_rows(b0)
    rows_notready = _parse_table_rows(b1)
    rows_pvc = _parse_table_rows(b_pvc)
    rows_post = _parse_table_rows(b_post)
    rows_final = _parse_table_rows(b_final)

    t_notready = _parse_wsrep_ts(b1)
    t_post = _parse_wsrep_ts(b_post)

    def iso_to_dt(s: str | None) -> datetime | None:
        if not s:
            return None
        try:
            return datetime.fromisoformat(s)
        except ValueError:
            try:
                return datetime.fromisoformat(s.replace("Z", "+00:00"))
            except ValueError:
                return None

    dt0 = iso_to_dt(t_notready)
    dt1 = iso_to_dt(t_post)
    if dt0 and dt1 and dt1 < dt0:
        dt1 = None

    lines_out: list[str] = []
    lines_out.append("# Disaster Recovery & Availability Report — Galera E2E (auto-generated)\n")
    lines_out.append(f"**Generated (UTC):** {datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')}\n")
    lines_out.append("## Source\n\n")
    lines_out.append(
        "This report is built from the **Galera PodRemediator E2E timeline** "
        "(Ansible appends one block per milestone: `# Stage: …`, MariaDB/wsrep output, then a filtered `oc get events` tail). "
        "Each step states **what the playbook did**, adds **Reading the Kubernetes events (plain language)** "
        "(heuristic interpretation — not a substitute for the raw lines), then **verbatim excerpts** and a "
        "**parsed wsrep table** for numeric comparison.\n"
    )

    _emit_step_section(
        lines_out,
        title="## STEP 1 — Baseline before fault injection",
        e2e_what=(
            "The E2E recorded cluster layout after confirming which worker hosts the test StatefulSet pod "
            "and optional colocation with other Galera members. **No failure yet**: this is the “steady state” "
            "reference for nodes, PVC/PV hints, and `wsrep_cluster_size`."
        ),
        block=b0,
        rows=rows_baseline,
        pvc_map=pvc_map,
        summary_note=_colocation_note(rows_baseline),
        k8s_stage="baseline",
    )

    _emit_step_section(
        lines_out,
        title="## STEP 2 — Simulated worker loss (virsh destroy) and node NotReady",
        e2e_what=(
            "Ansible ran **`virsh destroy`** on the libvirt domain for the worker that schedules `openstack-galera-0` "
            "(the E2E target pod), then waited until Kubernetes marked that node **NotReady**. "
            "This snapshot is taken **immediately after** that transition: expect mysql probes to fail on pods "
            "that lived on the dead worker and `wsrep_cluster_size` to read as “—” until the control plane and "
            "remaining members converge."
        ),
        block=b1,
        rows=rows_notready,
        pvc_map=pvc_map,
        summary_note=_quorum_note(rows_notready),
        k8s_stage="notready",
    )

    lines_out.append("## STEP 3 — Remediation stack (NHC / SNR) and PVC wait\n\n")
    lines_out.append("### What the E2E did at this milestone\n\n")
    lines_out.append(
        "While the node stays **NotReady**, Node Health Check / Self-Node Remediation machinery runs outside this log; "
        "the playbook snapshots **after NHC listing** (unhealthy node visible) and at the **start of the PVC poll** "
        "that waits until the test PVC is deleted or bound to a **new** `volumeName`. "
        "**PodRemediator** is expected to delete stale **local** PVCs on the failed node so StatefulSet controllers "
        "can re-provision volumes elsewhere.\n\n"
    )
    if b_nhc:
        ts = _stage_log_timestamp(b_nhc)
        if ts:
            lines_out.append(f"#### After NHC listing — recorded `{ts}`\n\n")
        lines_out.append(_md_fence("Galera / wsrep excerpt", _extract_wsrep_excerpt(b_nhc)))
        lines_out.append("\n")
        _kn = _summarize_k8s_events(_k8s_scan_tail(b_nhc), "nhc")
        if _kn.strip():
            lines_out.append("##### Reading the Kubernetes events (plain language)\n\n")
            lines_out.append(_kn + "\n")
        kl = _extract_k8s_evidence_lines(b_nhc)
        if kl:
            lines_out.append(_md_fence("Kubernetes events (keyword excerpt)", "\n".join(kl)))
        lines_out.append("\n")
        rows_nhc = _parse_table_rows(b_nhc)
        if rows_nhc:
            lines_out.append(_table_md(rows_nhc, pvc_map) + "\n\n")
    if b_poll:
        ts = _stage_log_timestamp(b_poll)
        if ts:
            lines_out.append(f"#### PVC deletion / volume-change poll — recorded `{ts}`\n\n")
        lines_out.append(_md_fence("Galera / wsrep excerpt", _extract_wsrep_excerpt(b_poll)))
        lines_out.append("\n")
        _kp = _summarize_k8s_events(_k8s_scan_tail(b_poll), "poll")
        if _kp.strip():
            lines_out.append("##### Reading the Kubernetes events (plain language)\n\n")
            lines_out.append(_kp + "\n")
        kl = _extract_k8s_evidence_lines(b_poll)
        if kl:
            lines_out.append(_md_fence("Kubernetes events (keyword excerpt)", "\n".join(kl)))
        lines_out.append("\n")
        rows_poll = _parse_table_rows(b_poll)
        if rows_poll:
            lines_out.append(_table_md(rows_poll, pvc_map) + "\n\n")

    lines_out.append("## STEP 4 — PVC remediated and workload rescheduled\n\n")
    lines_out.append("### What the E2E did at this milestone\n\n")
    lines_out.append(
        "After the PVC wait succeeds, Ansible captures **two** snapshots: (A) right when the test PVC is "
        "considered **remediated** (deleted or new `volumeName`), and (B) when the StatefulSet test pod is "
        "**Running** again on a surviving worker. Compare PVC → PV hints with STEP 1 to prove a new local volume.\n\n"
    )

    def _sub4(label: str, blk: str, rows: list[dict[str, Any]], stage_key: str) -> None:
        lines_out.append(f"#### {label}\n\n")
        ts = _stage_log_timestamp(blk)
        if ts:
            lines_out.append(f"**Recorded in timeline:** `{ts}`\n\n")
        lines_out.append(_md_fence("Galera / wsrep excerpt", _extract_wsrep_excerpt(blk)))
        lines_out.append("\n")
        _ks = _summarize_k8s_events(_k8s_scan_tail(blk), stage_key)
        if _ks.strip():
            lines_out.append("##### Reading the Kubernetes events (plain language)\n\n")
            lines_out.append(_ks + "\n")
        kl = _extract_k8s_evidence_lines(blk)
        if kl:
            lines_out.append(_md_fence("Kubernetes events (keyword excerpt)", "\n".join(kl)))
        else:
            lines_out.append(
                "**Kubernetes events**\n\n"
                "*(No keyword-filter hits in this slice; see full timeline file.)*\n\n"
            )
        if rows:
            lines_out.append("**wsrep summary (parsed)**\n\n")
            lines_out.append(_table_md(rows, pvc_map) + "\n\n")

    _sub4("A — PVC remediated (deleted or new volume)", b_pvc, rows_pvc, "pvc_rem")
    if b_post.strip():
        _sub4("B — StatefulSet test pod Running (post-reschedule)", b_post, rows_post, "post_resched")
    lines_out.append("### Interpretation\n\n")
    lines_out.append(
        "A **new** `pvc-…` UID for `mysql-db-openstack-galera-0` (vs STEP 1) confirms the operator bound a "
        "fresh TopolVM volume after PodRemediator cleared the old claim. Section B should show "
        "`wsrep_cluster_size` back to the StatefulSet replica count and **Primary / Synced** on reachable pods.\n\n"
    )

    _emit_step_section(
        lines_out,
        title="## STEP 5 — Worker powered back; node Ready",
        e2e_what=(
            "Ansible ran **`virsh start`** on the same worker domain and waited until the node reported **Ready**. "
            "Galera should already be healthy from STEP 4; this milestone confirms **infrastructure** recovery "
            "(compute node back) rather than database-specific repair."
        ),
        block=b_final,
        rows=rows_final,
        pvc_map=pvc_map,
        summary_note=(
            "If all ordinals show **Synced** / **Primary** and `wsrep_cluster_size` equals `spec.replicas`, "
            "the cluster tolerated the worker outage and the PVC remediation path for the E2E target completed."
        ),
        k8s_stage="final",
    )

    lines_out.append("## Availability window (approximate)\n\n")
    lines_out.append(
        f"- **From** post-failure wsrep snapshot (`{t_notready or '?'}`) **to** post-reschedule healthy snapshot "
        f"(`{t_post or '?'}`).\n"
    )
    lines_out.append(f"- **Duration:** {_duration_str(dt0, dt1)}\n")
    lines_out.append(
        "\n> Note: If mysql probes fail on all pods in STEP 2, treat that as **effective DB outage** even when "
        "the summary table still shows placeholders.\n"
    )

    return "\n".join(lines_out)


def main() -> int:
    if len(sys.argv) < 2:
        print("Usage: galera_e2e_dr_report.py <timeline_log_path> [output_md_path]", file=sys.stderr)
        return 2
    path = Path(sys.argv[1])
    if not path.is_file():
        print(f"Not a file: {path}", file=sys.stderr)
        return 1
    text = path.read_text(encoding="utf-8", errors="replace")
    md = generate_report(text)
    if len(sys.argv) >= 3:
        out = Path(sys.argv[2])
        out.write_text(md, encoding="utf-8")
        print(str(out))
    else:
        sys.stdout.write(md)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
