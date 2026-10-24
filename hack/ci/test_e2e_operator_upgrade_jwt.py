import copy
import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import MagicMock, call, patch


spec = importlib.util.spec_from_file_location(
    "operator_upgrade_jwt", Path(__file__).with_name("operator-upgrade-jwt-migration.py")
)
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)


class RoleMigrationTests(unittest.TestCase):
    def test_audience_change_preserves_custom_role_restrictions(self):
        role = {
            "bound_audiences": ["openbao-internal"],
            "bound_subject": "system:serviceaccount:operator:controller",
            "bound_claims": {"environment": ["validation"]},
            "token_policies": ["custom-operator", "approval"],
            "token_max_ttl": 900,
            "token_bound_cidrs": ["10.0.0.0/8"],
        }
        original = copy.deepcopy(role)
        api = object.__new__(migration.API)
        api.pod = "fixture-0"
        stored = {}

        def request(method, path, payload=None):
            self.assertEqual(path, migration.ROLE)
            if method == "POST":
                stored.update(payload)
                return {}
            return {"data": stored}

        with patch.object(api, "request", side_effect=request):
            updated = api.role_audiences(role, ["urn:openbao:controller:fixture"])
        self.assertEqual(role, original)
        self.assertEqual(stored, {**original, "bound_audiences": ["urn:openbao:controller:fixture"]})
        self.assertEqual(updated, stored)

    def test_old_or_unexpected_audience_fails_without_replaying_the_write(self):
        role = {"bound_audiences": ["shared"], "bound_subject": "controller"}
        for audience in ("shared", "unrelated"):
            with self.subTest(audience=audience):
                api = object.__new__(migration.API)
                api.pod = "fixture-2"
                actual = {**role, "bound_audiences": [audience]}
                with patch.object(api, "request", side_effect=[{}, {"data": actual}]) as request:
                    with self.assertRaisesRegex(RuntimeError, "changed restrictions at fixture-2: bound_audiences"):
                        api.role_audiences(role, ["target"])
                self.assertEqual([item.args[0] for item in request.call_args_list], ["POST", "GET"])

    def test_added_restriction_is_reported_by_name_without_its_value(self):
        api = object.__new__(migration.API)
        api.pod = "fixture-1"
        actual = {"bound_subject": "controller", "bound_audiences": ["target"], "extra": "private-value"}
        with patch.object(api, "request", side_effect=[{}, {"data": actual}]):
            with self.assertRaises(RuntimeError) as caught:
                api.role_audiences({"bound_subject": "controller"}, ["target"])
        self.assertEqual(str(caught.exception), "role update changed restrictions at fixture-1: extra")

    def test_server_side_loss_of_restrictions_fails_qualification(self):
        api = object.__new__(migration.API)
        api.pod = "fixture-0"
        with patch.object(api, "request", side_effect=[{}, {"data": {"bound_audiences": ["target"]}}]):
            with self.assertRaisesRegex(RuntimeError, "changed restrictions"):
                api.role_audiences({"bound_subject": "controller"}, ["target"])

    def test_empty_oidc_lists_match_server_normalization(self):
        role = {"bound_subject": "controller", "bound_audiences": ["shared"],
                "allowed_redirect_uris": None, "oidc_scopes": None, "oauth2_metadata": None}
        actual = {**role, "bound_audiences": ["target"],
                  "allowed_redirect_uris": [], "oidc_scopes": [], "oauth2_metadata": []}
        api = object.__new__(migration.API)
        api.pod = "fixture-0"
        with patch.object(api, "request", side_effect=[{}, {"data": actual}]):
            api.role_audiences(role, ["target"])
        self.assertIsNone(role["oidc_scopes"])

    def test_nonempty_oidc_restrictions_cannot_be_lost(self):
        for field in ("allowed_redirect_uris", "oidc_scopes", "oauth2_metadata"):
            with self.subTest(field=field):
                role = {"bound_subject": "controller", field: ["restriction"]}
                actual = {**role, field: [], "bound_audiences": ["target"]}
                api = object.__new__(migration.API)
                api.pod = "fixture-0"
                with patch.object(api, "request", side_effect=[{}, {"data": actual}]):
                    with self.assertRaisesRegex(RuntimeError, "changed restrictions"):
                        api.role_audiences(role, ["target"])

    def test_server_failure_is_not_accepted_as_rejected_replay(self):
        api = object.__new__(migration.API)
        api.pod = "fixture-0"
        api.port, api.context, api.token = 8200, None, "private-admin-token"
        api.pod = "fixture-0"
        with patch.object(migration, "FixtureConnection") as connection:
            healthy = MagicMock(status=200)
            healthy.read.return_value = json.dumps({"initialized": True, "sealed": False, "standby": False}).encode()
            failure = MagicMock(status=500)
            failure.read.return_value = json.dumps({"sensitive": "private-response"}).encode()
            connection.return_value.getresponse.side_effect = [healthy, failure]
            with self.assertRaises(RuntimeError) as caught:
                api.login("openbao-operator", "private-jwt", allowed=False)
        self.assertEqual(str(caught.exception), "POST auth/jwt-operator/login: HTTP 500")


class ActiveEndpointTests(unittest.TestCase):
    healthy = {"initialized": True, "sealed": False, "standby": False}

    def api(self):
        api = object.__new__(migration.API)
        api.port, api.context, api.token, api.pod = 8200, None, "private-admin-token", "fixture-2"
        return api

    def test_selects_each_fixture_leader_and_pins_original_cluster_uid(self):
        for index in range(3):
            with self.subTest(index=index):
                pod = f"{migration.CLUSTER}-{index}"
                cluster = {"metadata": {"uid": "original"}, "status": {"activeLeader": pod}}
                self.assertEqual(migration.leader_pod(cluster, "original"), pod)
                with self.assertRaisesRegex(RuntimeError, "identity changed"):
                    migration.leader_pod(cluster, "replacement")

    def test_missing_or_foreign_leader_never_falls_back_to_pod_zero(self):
        for leader in (None, "", "another-cluster-0", f"{migration.CLUSTER}-3", f"{migration.CLUSTER}-0/extra"):
            with self.subTest(leader=leader):
                cluster = {"metadata": {"uid": "original"}, "status": {"activeLeader": leader}}
                with self.assertRaisesRegex(RuntimeError, "does not identify"):
                    migration.leader_pod(cluster, "original")

    def test_port_forward_uses_the_selected_pod_and_cleans_up(self):
        pod = f"{migration.CLUSTER}-2"
        with patch.object(migration.subprocess, "Popen") as popen, \
                patch.object(migration.selectors, "DefaultSelector"):
            process = popen.return_value
            process.stdout.readline.return_value = "Forwarding from 127.0.0.1:12345 -> 8200"
            with migration.port_forward(pod) as port:
                self.assertEqual(port, 12345)
            self.assertIn(f"pod/{pod}", popen.call_args.args[0])
            process.terminate.assert_called_once_with()
            process.wait.assert_called_once_with(timeout=10)
            process.stdout.close.assert_called_once_with()

    def test_checks_local_health_before_and_after_one_operation(self):
        api = self.api()
        payload = {"bound_audiences": ["target"]}
        with patch.object(api, "_request", side_effect=[self.healthy, {"ok": True}, self.healthy]) as request:
            self.assertEqual(api.request("POST", migration.ROLE, payload), {"ok": True})
        self.assertEqual(request.call_args_list, [
            call("GET", "sys/health", token="", expected=(200,)),
            call("POST", migration.ROLE, payload, None, (200, 204)),
            call("GET", "sys/health", token="", expected=(200,)),
        ])

    def test_malformed_or_nonactive_health_blocks_the_operation(self):
        cases = [{}, [], None, {**self.healthy, "standby": True}, {**self.healthy, "sealed": True},
                 {**self.healthy, "initialized": False}, {**self.healthy, "standby": 0},
                 {**self.healthy, "sealed": 0}, {**self.healthy, "initialized": 1}]
        for health in cases:
            with self.subTest(health=health):
                api = self.api()
                with patch.object(api, "_request", return_value=health) as request:
                    with self.assertRaisesRegex(RuntimeError, "fixture-2.*not initialized, unsealed and active"):
                        api.request("POST", migration.ROLE, {})
                request.assert_called_once_with("GET", "sys/health", token="", expected=(200,))

    def test_stale_leader_status_and_unhealthy_http_fail_before_the_operation(self):
        for status in (429, 503, 501, 500):
            with self.subTest(status=status):
                api = self.api()
                with patch.object(migration, "FixtureConnection") as connection:
                    response = connection.return_value.getresponse.return_value
                    response.status = status
                    response.read.return_value = json.dumps({"sensitive": "private-response"}).encode()
                    with self.assertRaisesRegex(RuntimeError, f"fixture-2.*GET sys/health: HTTP {status}"):
                        api.request("POST", migration.ROLE, {})
                    connection.return_value.request.assert_called_once_with(
                        "GET", "/v1/sys/health", None,
                        {"X-Vault-Token": "", "Content-Type": "application/json"})

    def test_leadership_loss_after_write_stops_without_replay(self):
        api = self.api()
        with patch.object(api, "_request", side_effect=[self.healthy, {}, {**self.healthy, "standby": True}]) as request:
            with self.assertRaisesRegex(RuntimeError, "fixture-2.*not initialized, unsealed and active"):
                api.request("POST", migration.ROLE, {})
        self.assertEqual([item.args[:2] for item in request.call_args_list], [
            ("GET", "sys/health"), ("POST", migration.ROLE), ("GET", "sys/health")])

    def test_rejected_login_still_requires_active_endpoint_afterward(self):
        for status in (400, 403):
            with self.subTest(status=status):
                api = self.api()
                with patch.object(migration, "FixtureConnection") as connection:
                    responses = []
                    for code, body in [(200, self.healthy), (status, {"errors": ["rejected"]}),
                                       (429, {**self.healthy, "standby": True})]:
                        response = MagicMock(status=code)
                        response.read.return_value = json.dumps(body).encode()
                        responses.append(response)
                    connection.return_value.getresponse.side_effect = responses
                    with self.assertRaisesRegex(RuntimeError, "fixture-2.*GET sys/health: HTTP 429"):
                        api.login("operator", "private-jwt", allowed=False)
                    self.assertEqual(connection.return_value.request.call_count, 3)


if __name__ == "__main__":
    unittest.main()
