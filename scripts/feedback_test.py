import datetime
import os
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import feedback  # noqa: E402


class SigV4(unittest.TestCase):
    def test_aws_published_vector(self):
        # IAM ListUsers example from the AWS SigV4 documentation.
        headers = {
            "content-type": "application/x-www-form-urlencoded; charset=utf-8",
            "host": "iam.amazonaws.com",
            "x-amz-date": "20150830T123600Z",
        }
        sig, signed, scope = feedback.sign(
            "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "GET", "iam.amazonaws.com",
            "/", "Action=ListUsers&Version=2010-05-08", headers, b"",
            "20150830T123600Z", "us-east-1", "iam")
        self.assertEqual(sig, "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7")
        self.assertEqual(signed, "content-type;host;x-amz-date")


class Bodies(unittest.TestCase):
    def test_query_body(self):
        b = feedback.query_body("t", 1700000000000, "new")
        self.assertFalse(b["ScanIndexForward"])
        self.assertEqual(b["KeyConditionExpression"], "pk = :p AND sk >= :since")
        self.assertEqual(b["ExpressionAttributeValues"][":since"], {"S": "1700000000000"})
        self.assertEqual(b["FilterExpression"], "#s = :s")

    def test_query_body_plain(self):
        b = feedback.query_body("t")
        self.assertEqual(b["KeyConditionExpression"], "pk = :p")
        self.assertNotIn("FilterExpression", b)

    def test_update_body_never_creates_and_reads_nothing(self):
        b = feedback.update_body("t", "1#2", "triaged", "n", "agent", "2025-01-01T00:00:00Z")
        self.assertEqual(b["ConditionExpression"], "attribute_exists(sk)")
        self.assertEqual(b["ReturnValues"], "NONE")
        self.assertEqual(b["Key"], {"pk": {"S": "feedback"}, "sk": {"S": "1#2"}})
        self.assertNotIn("text", b["UpdateExpression"])


class Decode(unittest.TestCase):
    def test_decode(self):
        got = feedback.decode_item({"a": {"S": "x"}, "b": {"BOOL": True}, "c": {"N": "3"},
                                    "d": {"L": [{"S": "y"}]}, "e": {"M": {"k": {"NULL": True}}}})
        self.assertEqual(got, {"a": "x", "b": True, "c": 3, "d": ["y"], "e": {"k": None}})


class Args(unittest.TestCase):
    def test_since_days(self):
        now = datetime.datetime(2025, 1, 8, tzinfo=datetime.timezone.utc)
        self.assertEqual(feedback.parse_since("7d", now), int(datetime.datetime(2025, 1, 1, tzinfo=datetime.timezone.utc).timestamp() * 1000))

    def test_since_date(self):
        self.assertEqual(feedback.parse_since("2025-01-01"), 1735689600000)

    def test_since_bad(self):
        with self.assertRaises(SystemExit):
            feedback.parse_since("last week")

    def test_bad_status_rejected_without_request(self):
        with mock.patch.object(feedback, "post") as post:
            with self.assertRaises(SystemExit):
                feedback.main(["set-status", "1#2", "bogus"])
            post.assert_not_called()

    def test_set_status_sends_update(self):
        with mock.patch.object(feedback, "post", return_value={}) as post:
            feedback.main(["set-status", "1#2", "filed", "--note", "danbot-23"])
        op, body = post.call_args[0]
        self.assertEqual(op, "UpdateItem")
        self.assertEqual(body["ExpressionAttributeValues"][":s"], {"S": "filed"})

    def test_list_skips_stubs_and_follows_pages(self):
        pages = [
            {"Items": [{"sk": {"S": "2"}, "created_at": {"S": "x"}, "text": {"S": "hi"}}], "LastEvaluatedKey": {"sk": {"S": "2"}}},
            {"Items": [{"sk": {"S": "1"}, "status": {"S": "new"}}]},
        ]
        with mock.patch.object(feedback, "post", side_effect=pages) as post, mock.patch("builtins.print") as pr:
            feedback.main(["list"])
        self.assertEqual(post.call_count, 2)
        printed = [c.args[0] for c in pr.call_args_list if not c.kwargs]
        self.assertEqual(len(printed), 1)


if __name__ == "__main__":
    unittest.main()
