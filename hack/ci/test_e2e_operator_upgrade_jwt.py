import copy
import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import patch


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
        stored = {}

        def request(method, path, payload=None):
            self.assertEqual(path, migration.ROLE)
            if method == "POST":
                stored.update(payload)
                return {}
            return {"data": stored}

        with patch.object(api, "request", side_effect=request):
            api.role_audiences(role, ["urn:openbao:controller:fixture"])
        self.assertEqual(role, original)
        self.assertEqual(stored, {**original, "bound_audiences": ["urn:openbao:controller:fixture"]})

    def test_server_side_loss_of_restrictions_fails_qualification(self):
        api = object.__new__(migration.API)
        with patch.object(api, "request", side_effect=[{}, {"data": {"bound_audiences": ["target"]}}]):
            with self.assertRaisesRegex(RuntimeError, "changed restrictions"):
                api.role_audiences({"bound_subject": "controller"}, ["target"])

    def test_empty_oidc_lists_match_server_normalization(self):
        role = {"bound_subject": "controller", "bound_audiences": ["shared"],
                "allowed_redirect_uris": None, "oidc_scopes": None, "oauth2_metadata": None}
        actual = {**role, "bound_audiences": ["target"],
                  "allowed_redirect_uris": [], "oidc_scopes": [], "oauth2_metadata": []}
        api = object.__new__(migration.API)
        with patch.object(api, "request", side_effect=[{}, {"data": actual}]):
            api.role_audiences(role, ["target"])
        self.assertIsNone(role["oidc_scopes"])

    def test_nonempty_oidc_restrictions_cannot_be_lost(self):
        for field in ("allowed_redirect_uris", "oidc_scopes", "oauth2_metadata"):
            with self.subTest(field=field):
                role = {"bound_subject": "controller", field: ["restriction"]}
                actual = {**role, field: [], "bound_audiences": ["target"]}
                api = object.__new__(migration.API)
                with patch.object(api, "request", side_effect=[{}, {"data": actual}]):
                    with self.assertRaisesRegex(RuntimeError, "changed restrictions"):
                        api.role_audiences(role, ["target"])

    def test_server_failure_is_not_accepted_as_rejected_replay(self):
        api = object.__new__(migration.API)
        api.port, api.context, api.token = 8200, None, "private-admin-token"
        with patch.object(migration, "FixtureConnection") as connection:
            response = connection.return_value.getresponse.return_value
            response.status = 500
            response.read.return_value = json.dumps({"sensitive": "private-response"}).encode()
            with self.assertRaises(RuntimeError) as caught:
                api.login("openbao-operator", "private-jwt", allowed=False)
        self.assertEqual(str(caught.exception), "POST auth/jwt-operator/login: HTTP 500")


if __name__ == "__main__":
    unittest.main()
