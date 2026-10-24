#!/usr/bin/env python3
"""Opt a previous-release Hardened fixture into Target after the operator upgrade.

Run only from operator-upgrade-e2e.sh in its disposable Kind cluster. Credentials
stay in memory. The API connection verifies the fixture CA and service DNS name.
"""

import base64
import contextlib
import http.client
import json
import os
import re
import selectors
import socket
import ssl
import subprocess
import time


NAMESPACE = "openbao-upgrade-e2e"
CLUSTER = "operator-upgrade-hardened"
OPERATOR_NAMESPACE = "openbao-operator-system"
ROLE = "auth/jwt-operator/role/openbao-operator"
AUTOPILOT = "sys/storage/raft/autopilot/configuration"
SHARED = "openbao-internal"


def kubectl(*args):
    return subprocess.check_output(
        [os.environ.get("KUBECTL", "kubectl"), *args], stderr=subprocess.PIPE, text=True, timeout=60
    ).strip()


def resource(kind, name, namespace=NAMESPACE):
    return json.loads(kubectl("get", kind, name, "-n", namespace, "-o", "json"))


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def canonical_role(role):
    # OpenBao 2.6.2 writes these empty OIDC lists as [] after reading them as null.
    result = dict(role)
    for field in ("allowed_redirect_uris", "oidc_scopes", "oauth2_metadata"):
        if field in result and result[field] is None:
            result[field] = []
    return result


def leader_pod(cluster, uid):
    require(cluster["metadata"]["uid"] == uid, "legacy fixture identity changed before migration")
    leader = cluster.get("status", {}).get("activeLeader", "")
    require(leader in {f"{CLUSTER}-{index}" for index in range(3)},
            "legacy fixture does not identify one of its three Pods as active leader")
    return leader


@contextlib.contextmanager
def port_forward(pod):
    process = subprocess.Popen(
        [os.environ.get("KUBECTL", "kubectl"), "port-forward", "-n", NAMESPACE,
         f"pod/{pod}", ":8200", "--address=127.0.0.1"],
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
    )
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            require(selector.select(timeout=30), "port-forward did not become ready")
            line = process.stdout.readline()
        match = re.search(r"Forwarding from 127\.0\.0\.1:(\d+)", line)
        require(match, "port-forward did not report a loopback port")
        yield int(match.group(1))
    finally:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=10)
        process.stdout.close()


class FixtureConnection(http.client.HTTPSConnection):
    def connect(self):
        connection = socket.create_connection(("127.0.0.1", self.port), timeout=self.timeout)
        self.sock = self._context.wrap_socket(connection, server_hostname=self.host)


class API:
    def __init__(self, port, ca, pod):
        self.port, self.context, self.token = port, ssl.create_default_context(cadata=ca), ""
        self.pod = pod

    def request(self, method, path, payload=None, token=None, expected=(200, 204)):
        self.require_active()
        result = self._request(method, path, payload, token, expected)
        self.require_active()
        return result

    def require_active(self):
        # This endpoint is local even when logical requests would be forwarded.
        # Do not allow standbyok or send the fixture's administrator credential.
        try:
            health = self._request("GET", "sys/health", token="", expected=(200,))
        except RuntimeError as error:
            raise RuntimeError(f"migration endpoint {self.pod} is not verified active: {error}") from error
        require(isinstance(health, dict) and health.get("initialized") is True and health.get("sealed") is False
                and health.get("standby") is False,
                f"migration endpoint {self.pod} is not initialized, unsealed and active")

    def _request(self, method, path, payload=None, token=None, expected=(200, 204)):
        connection = FixtureConnection(
            f"{CLUSTER}.{NAMESPACE}.svc", self.port, context=self.context, timeout=30
        )
        try:
            connection.request(method, "/v1/" + path,
                               None if payload is None else json.dumps(payload),
                               {"X-Vault-Token": self.token if token is None else token,
                                "Content-Type": "application/json"})
            response = connection.getresponse()
            body = response.read(1024 * 1024)
            require(response.status in expected, f"{method} {path}: HTTP {response.status}")
            return json.loads(body) if body else {}
        finally:
            connection.close()

    def login(self, role, jwt, allowed=True):
        return self.request("POST", "auth/jwt-operator/login", {"role": role, "jwt": jwt}, token="",
                            expected=(200,) if allowed else (400, 403))

    def role_audiences(self, role, audiences):
        self.request("POST", ROLE, {**role, "bound_audiences": audiences})
        expected = canonical_role({**role, "bound_audiences": audiences})
        missing = object()
        actual = self.request("GET", ROLE)["data"]
        normalized = canonical_role(actual)
        changed = sorted(field for field in expected.keys() | normalized.keys()
                         if expected.get(field, missing) != normalized.get(field, missing))
        require(not changed, f"role update changed restrictions at {self.pod}: " + ", ".join(changed))
        return actual


def controller_token(pod, audience):
    return kubectl("create", "token", pod["spec"]["serviceAccountName"], "-n", OPERATOR_NAMESPACE,
                   f"--audience={audience}", "--duration=10m", "--bound-object-kind=Pod",
                   f"--bound-object-name={pod['metadata']['name']}",
                   f"--bound-object-uid={pod['metadata']['uid']}")


def main():
    cluster = resource("openbaocluster", CLUSTER)
    uid = cluster["metadata"]["uid"]
    require(cluster["spec"].get("controllerJWTMode", "Shared") == "Shared",
            "migration requires a legacy Shared fixture")
    require(cluster["status"].get("initialized"), "legacy fixture is not initialized")
    pods = json.loads(kubectl("get", "pods", "-n", OPERATOR_NAMESPACE,
                             "-l", "app.kubernetes.io/component=controller", "-o", "json"))["items"]
    require(len(pods) == 1, "expected one controller Pod")
    pod = pods[0]
    audience = "urn:openbao:controller:" + uid
    shared = controller_token(pod, SHARED)
    target = controller_token(pod, audience)
    unrelated = controller_token(pod, "urn:openbao:controller:unrelated-" + uid)
    admin_jwt = kubectl("create", "token", "default", "-n", NAMESPACE,
                        f"--audience={SHARED}", "--duration=10m")
    ca = base64.b64decode(resource("secret", CLUSTER + "-tls-ca")["data"]["ca.crt"]).decode()
    leader = leader_pod(resource("openbaocluster", CLUSTER), uid)
    with port_forward(leader) as port:
        api = API(port, ca, leader)
        api.token = api.login("operator-upgrade-hardened", admin_jwt)["auth"]["client_token"]
        print(json.dumps({"controllerJWTMigrationEndpoint": leader, "verifiedActive": True}))
        role = api.request("GET", ROLE)["data"]
        require(role["bound_audiences"] == [SHARED], "legacy role does not use shared trust")
        require(role["bound_subject"] == f"system:serviceaccount:{OPERATOR_NAMESPACE}:{pod['spec']['serviceAccountName']}",
                "legacy role is bound to the wrong controller identity")
        api.login("openbao-operator", shared)
        api.login("openbao-operator", target, allowed=False)
        role = api.role_audiences(role, [SHARED, audience])
        api.login("openbao-operator", target)
        patch = [{"op": "test", "path": "/metadata/uid", "value": uid},
                 {"op": "add", "path": "/spec/controllerJWTMode", "value": "Target"}]
        kubectl("patch", "openbaocluster", CLUSTER, "-n", NAMESPACE,
                "--type=json", "-p", json.dumps(patch))
        api.role_audiences(role, [audience])
        api.login("openbao-operator", target)
        api.login("openbao-operator", shared, allowed=False)
        api.login("openbao-operator", unrelated, allowed=False)
        api.request("POST", AUTOPILOT, {"min_quorum": 99})
        kubectl("annotate", "openbaocluster", CLUSTER, "-n", NAMESPACE,
                f"e2e.openbao.org/reconcile-trigger={time.time_ns()}", "--overwrite")
        deadline = time.monotonic() + 180
        while api.request("GET", AUTOPILOT)["data"]["min_quorum"] != 3:
            require(time.monotonic() < deadline, "controller did not repair Autopilot after migration")
            time.sleep(2)
        value = api.request("GET", "secret/data/operator-upgrade-hardened")["data"]["data"]["value"]
        require(value == "preserved", "pre-upgrade data changed during JWT migration")
        actual = resource("openbaocluster", CLUSTER)
        require(actual["metadata"]["uid"] == uid and actual["spec"]["controllerJWTMode"] == "Target",
                "migration changed cluster identity or lost Target mode")
        api.require_active()
        print(json.dumps({"controllerJWTMigration": "passed", "clusterUID": uid,
                          "adminPod": leader,
                          "sourceMode": "Shared", "destinationMode": "Target",
                          "sharedAndUnrelatedJWTsRejected": True, "autopilotRepair": "passed",
                          "preUpgradeData": "preserved"}))


if __name__ == "__main__":
    main()
