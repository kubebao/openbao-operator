#!/usr/bin/env python3
"""Export one matrix row for the shared E2E action without rerouting it."""

import json
import os
from pathlib import Path


def configure():
    transport = os.environ.get("E2E_IMAGE_TRANSPORT", "registry")
    if transport not in ("registry", "artifact"):
        raise ValueError("unknown candidate image transport")
    row = json.loads(os.environ["LANE_JSON"])
    expected = row["expected_case_ids"]
    if not expected or not row["label_filter"]:
        raise ValueError("planned selector and scenario IDs are required")
    seal = row["id"] == "hsm"
    if seal and (not os.environ["OPENBAO_IMAGE"] or not os.environ["KMIP_SERVER_IMAGE"]):
        raise ValueError("seal lanes require exact per-run SoftHSM and PyKMIP image references")
    hardened_init = os.environ["HARDENED_INIT_IMAGE"] or row["hardened_init_image"]
    hardened_upgrade = os.environ["HARDENED_UPGRADE_IMAGE"] or row["hardened_upgrade_executor_image"]
    report = Path(os.environ["REPORT_DIR"])
    report.mkdir(parents=True, exist_ok=True)
    lane_name = row["name"]
    if row.get("profile"):
        lane_name += f" ({row['profile']} / {row['coverage']})"
    values = {
        "E2E_BINARY_DIR": str(Path(os.environ["RUNNER_TEMP"]) / "e2e-binaries"),
        "E2E_SOURCE_SHA": os.environ["GITHUB_SHA"],
        "E2E_ARTIFACT_NAME": os.environ["ARTIFACT_NAME"],
        "E2E_LANE_NAME": lane_name,
        "EXPECTED_CASE_IDS": json.dumps(expected),
        "E2E_REPORT_DIR": str(report),
        "E2E_JUNIT_REPORT": str(report / "junit.xml"),
        "E2E_JSON_REPORT": str(report / "ginkgo.json"),
        "E2E_SUMMARY_REPORT": str(report / "summary.md"),
        "E2E_SUMMARY_JSON": str(report / "summary.json"),
        "KIND_NODE_IMAGE": row["kind_node_image"],
        "KIND_CLUSTER": "kind",
        "OPENBAO_REQUEUE_STANDARD": "10s",
        "E2E_LABEL_FILTER": row["label_filter"],
        "E2E_PARALLEL_NODES": str(row["parallel_nodes"]),
        "E2E_TIMEOUT": row["e2e_timeout"],
        "E2E_FAIL_ON_EMPTY": "true",
        "E2E_POLL_PROGRESS_AFTER": "10m",
        "E2E_SKIP_IMAGE_BUILD": "true",
        # The action collects diagnostics before deleting the cluster, also after failures.
        "E2E_SKIP_CLEANUP": "true",
        "E2E_DRY_RUN": "false",
        "E2E_INSTALL_CSI_HOSTPATH": row["install_csi_hostpath"],
        "E2E_ENABLE_HARDENED_SIGNED_SUITE": row["hardened_signed"],
        "E2E_ENABLE_SOFTHSM_SUITE": str(seal).lower(),
        "E2E_ENABLE_KMIP_SUITE": str(seal).lower(),
        "E2E_OPERATOR_IMAGE": os.environ["MANAGER_IMAGE"],
        "E2E_CONFIG_INIT_IMAGE": os.environ["CONFIG_INIT_IMAGE"],
        "E2E_BACKUP_EXECUTOR_IMAGE": os.environ["BACKUP_EXECUTOR_IMAGE"],
        "E2E_UPGRADE_EXECUTOR_IMAGE": hardened_upgrade or os.environ["UPGRADE_EXECUTOR_IMAGE"],
        "E2E_HARDENED_CONFIG_INIT_IMAGE": hardened_init,
        "E2E_HARDENED_UPGRADE_EXECUTOR_IMAGE": hardened_upgrade,
        "E2E_OPENBAO_IMAGE": os.environ["OPENBAO_IMAGE"] or row["openbao_image"],
        "E2E_KMIP_SERVER_IMAGE": os.environ["KMIP_SERVER_IMAGE"],
        "E2E_RUSTFS_IMAGE": row["rustfs_image"],
        "E2E_FAKE_GCS_IMAGE": row["fake_gcs_image"],
        "E2E_AZURITE_IMAGE": row["azurite_image"],
    }
    for key in (
        "load_backup_executor_image", "load_upgrade_executor_image", "preload_upgrade_images",
        "preload_hardened_assets", "preload_storage_emulators", "preload_rustfs_image",
        "preload_fake_gcs_image", "preload_azurite_image",
    ):
        values["E2E_" + key.upper()] = row[key]
    if os.environ["RELEASE_OPERATOR_VERSION"]:
        values["OPERATOR_VERSION"] = os.environ["RELEASE_OPERATOR_VERSION"]
    with Path(os.environ["GITHUB_ENV"]).open("a") as output:
        for key, value in values.items():
            if "\n" in value or "\r" in value:
                raise ValueError(f"E2E environment value {key} must fit on one line")
            output.write(f"{key}={value}\n")
    (report / "expected-cases.json").write_text(json.dumps(expected, indent=2) + "\n")


if __name__ == "__main__":
    configure()
