# Read-only role for the "Terraform Plan" workflow (.github/workflows/terraform-plan.yml).
# Pull requests assume it instead of the apply role (AWS_ROLE_TO_ASSUME), which has
# AdministratorAccess and is not defined in this repo.
#
# The GitHub OIDC provider already exists in the account (the apply role uses it),
# so it is looked up, not created.
data "aws_iam_openid_connect_provider" "github" {
  url = "https://token.actions.githubusercontent.com"
}

data "aws_iam_policy_document" "plan_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [data.aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    # Only pull_request runs of this repo. The workflow additionally refuses fork
    # PRs, and GitHub gives fork PRs no OIDC token at all. Branch pushes, the
    # production environment and other workflows do not match this subject.
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${local.github_owner}/${local.service}:pull_request"]
    }
  }
}

resource "aws_iam_role" "plan" {
  name                 = "${local.name_prefix}-tf-plan"
  description          = "Read-only: terraform plan from pull requests"
  assume_role_policy   = data.aws_iam_policy_document.plan_assume.json
  max_session_duration = 3600
}

resource "aws_iam_role_policy_attachment" "plan_read_only" {
  role       = aws_iam_role.plan.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

resource "aws_iam_role_policy" "plan_extra" {
  name = "plan"
  role = aws_iam_role.plan.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        # The aws_ssm_parameter resources are read with decryption. The state holds
        # those values too, so this adds no reach beyond the state read.
        Sid      = "DecryptSsmParameters"
        Effect   = "Allow"
        Action   = "kms:Decrypt"
        Resource = "*"
        Condition = {
          StringEquals = { "kms:ViaService" = "ssm.${data.aws_region.current.region}.amazonaws.com" }
        }
      },
      {
        # ReadOnlyAccess also allows reading every S3 object and DynamoDB item.
        # Plan needs neither the images nor the conversations, so deny them. The
        # state bucket is a different bucket and stays readable.
        Sid    = "DenyDataPlaneReads"
        Effect = "Deny"
        Action = [
          "s3:GetObject",
          "s3:GetObjectVersion",
          "dynamodb:GetItem",
          "dynamodb:BatchGetItem",
          "dynamodb:Query",
          "dynamodb:Scan",
          "dynamodb:PartiQLSelect",
          "dynamodb:ExportTableToPointInTime",
        ]
        Resource = [
          "${aws_s3_bucket.images.arn}/*",
          aws_dynamodb_table.conversations.arn,
          "${aws_dynamodb_table.conversations.arn}/index/*",
          aws_dynamodb_table.flags.arn,
          "${aws_dynamodb_table.flags.arn}/index/*",
        ]
      },
    ]
  })
}

output "plan_role_arn" {
  description = "Set as the AWS_PLAN_ROLE_TO_ASSUME GitHub secret for the Terraform Plan workflow."
  value       = aws_iam_role.plan.arn
}
