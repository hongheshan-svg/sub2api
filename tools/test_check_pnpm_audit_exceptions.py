#!/usr/bin/env python3
"""Tests for check_pnpm_audit_exceptions.py.

Run: python -m unittest discover -s tools -p 'test_*.py'
"""
import contextlib
import io
import json
import os
import sys
import tempfile
import unittest
from datetime import date, timedelta
from unittest import mock

import check_pnpm_audit_exceptions as checker


FUTURE = (date.today() + timedelta(days=30)).isoformat()

EXCEPTIONS_YAML = f"""version: 1
exceptions:
  - package: xlsx
    advisory: "GHSA-4r6h-8v6p-xvw6"
    severity: high
    reason: "test"
    mitigation: "test"
    expires_on: "{FUTURE}"
"""


def advisory(module, ghsa, severity="high"):
    return {"module_name": module, "severity": severity, "github_advisory_id": ghsa, "title": f"{module} issue"}


class CheckAuditExceptionsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.exceptions = os.path.join(self.tmp.name, "exceptions.yml")
        with open(self.exceptions, "w", encoding="utf-8") as handle:
            handle.write(EXCEPTIONS_YAML)

    def run_checker(self, audit_content):
        audit = os.path.join(self.tmp.name, "audit.json")
        with open(audit, "w", encoding="utf-8") as handle:
            handle.write(audit_content if isinstance(audit_content, str) else json.dumps(audit_content))
        stdout, stderr = io.StringIO(), io.StringIO()
        argv = ["check", "--audit", audit, "--exceptions", self.exceptions]
        with mock.patch.object(sys, "argv", argv), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            code = checker.main()
        return code, stdout.getvalue() + stderr.getvalue()

    def test_audit_error_output_fails(self):
        # pnpm audit 失败时输出 {"error": ...}，不能被当成零漏洞放行。
        code, out = self.run_checker(
            {"error": {"code": "ERR_PNPM_AUDIT_ENDPOINT_NOT_EXISTS", "message": "The audit endpoint doesn't exist."}}
        )
        self.assertEqual(code, 1)
        self.assertIn("pnpm audit failed", out)
        self.assertIn("ERR_PNPM_AUDIT_ENDPOINT_NOT_EXISTS", out)

    def test_non_dict_error_fails(self):
        code, out = self.run_checker({"error": "registry unreachable"})
        self.assertEqual(code, 1)
        self.assertIn("registry unreachable", out)

    def test_unrecognized_output_fails(self):
        code, out = self.run_checker({"metadata": {}})
        self.assertEqual(code, 1)
        self.assertIn("refusing to treat it as clean", out)

    def test_non_object_output_fails(self):
        code, out = self.run_checker([])
        self.assertEqual(code, 1)
        self.assertIn("not a JSON object", out)

    def test_empty_or_invalid_file_fails(self):
        for content in ("", "not json"):
            with self.subTest(content=content):
                code, out = self.run_checker(content)
                self.assertEqual(code, 1)
                self.assertIn("Cannot read pnpm audit output", out)

    def test_clean_audit_passes(self):
        code, out = self.run_checker({"actions": [], "advisories": {}, "metadata": {}})
        self.assertEqual(code, 0)
        self.assertIn("Audit exceptions validated.", out)

    def test_excepted_high_vulnerability_passes(self):
        code, _ = self.run_checker({"advisories": {"1": advisory("xlsx", "GHSA-4r6h-8v6p-xvw6")}})
        self.assertEqual(code, 0)

    def test_unexcepted_high_vulnerability_fails(self):
        code, out = self.run_checker({"advisories": {"1": advisory("axios", "GHSA-c29m-xwm3-cm6r")}})
        self.assertEqual(code, 1)
        self.assertIn("missing exceptions", out)
        self.assertIn("GHSA-c29m-xwm3-cm6r", out)

    def test_vulnerabilities_format_is_recognized(self):
        code, out = self.run_checker(
            {"vulnerabilities": {"axios": {"severity": "high", "via": [{"url": "https://github.com/advisories/GHSA-x"}]}}}
        )
        self.assertEqual(code, 1)
        self.assertIn("axios", out)


if __name__ == "__main__":
    unittest.main()
