# Access for the Trellis agents that work on this bot (read-only debugging).
#
# Rules for this file:
#   - one aws_iam_user_policy per capability, so each can be reviewed, added
#     or removed on its own;
#   - read-only, except the feedback status write below;
#   - never ssm:SendCommand, Session Manager or anything else that runs code
#     or reads secrets;
#   - no aws_iam_access_key resource: its secret would land in the terraform
#     state, which the PR plan role can read. The operator creates the key
#     out of band (see the README, "Feedback (operator)").
resource "aws_iam_user" "agent" {
  name = "${local.name_prefix}-agent"
  path = "/agents/"
}

resource "aws_iam_user_policy" "agent_feedback" {
  name = "feedback"
  user = aws_iam_user.agent.name

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        # No Scan: agents read through the one partition with Query.
        Sid      = "FeedbackRead"
        Effect   = "Allow"
        Action   = ["dynamodb:Query", "dynamodb:GetItem", "dynamodb:DescribeTable"]
        Resource = aws_dynamodb_table.feedback.arn
      },
      {
        # Status fields only. The key attributes must be listed or UpdateItem
        # is refused. ReturnValues is limited so a write cannot read back the
        # whole item. UpdateItem can create an item (IAM cannot stop that);
        # scripts/feedback.py always sends attribute_exists(sk).
        Sid      = "FeedbackSetStatus"
        Effect   = "Allow"
        Action   = ["dynamodb:UpdateItem"]
        Resource = aws_dynamodb_table.feedback.arn
        Condition = {
          "ForAllValues:StringEquals" = {
            "dynamodb:Attributes" = ["pk", "sk", "status", "status_note", "status_by", "status_at"]
          }
          StringEquals = {
            "dynamodb:ReturnValues" = ["NONE", "UPDATED_OLD", "UPDATED_NEW"]
          }
        }
      },
    ]
  })
}

output "agent_user_name" {
  description = "IAM user for the Trellis agents; create its access key by hand."
  value       = aws_iam_user.agent.name
}
