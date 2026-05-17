data "aws_iam_policy_document" "ecs_tasks_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "execution" {
  name               = "${local.name_prefix}-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

resource "aws_iam_role_policy_attachment" "execution_managed" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "execution_ssm" {
  name = "ssm-read"
  role = aws_iam_role.execution.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = ["ssm:GetParameters", "ssm:GetParameter"]
      Resource = [
        aws_ssm_parameter.discord_token.arn,
        aws_ssm_parameter.openai_token.arn,
        aws_ssm_parameter.otel_config.arn,
      ]
    }]
  })
}

resource "aws_iam_role" "task" {
  name               = "${local.name_prefix}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

resource "aws_iam_role_policy" "task_dynamodb" {
  name = "dynamodb"
  role = aws_iam_role.task.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "DDBActions"
        Effect = "Allow"
        Action = [
          "dynamodb:BatchGet*",
          "dynamodb:DescribeStream",
          "dynamodb:DescribeTable",
          "dynamodb:Get*",
          "dynamodb:Query",
          "dynamodb:Scan",
          "dynamodb:BatchWrite*",
          "dynamodb:Create*",
          "dynamodb:Delete*",
          "dynamodb:Update*",
          "dynamodb:PutItem",
        ]
        Resource = aws_dynamodb_table.conversations.arn
      },
      {
        Sid      = "DDBLSIActions"
        Effect   = "Allow"
        Action   = ["dynamodb:Query", "dynamodb:Scan"]
        Resource = "${aws_dynamodb_table.conversations.arn}/index/*"
      },
    ]
  })
}

resource "aws_iam_role_policy" "task_s3" {
  name = "s3"
  role = aws_iam_role.task.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "S3ObjectActions"
        Effect = "Allow"
        Action = [
          "s3:GetObject",
          "s3:PutObject",
          "s3:PutObjectACL",
          "s3:PutObjectTagging",
          "s3:DeleteObject",
          "s3:RestoreObject",
        ]
        Resource = "${aws_s3_bucket.images.arn}/*"
      },
      {
        Sid      = "S3ListAction"
        Effect   = "Allow"
        Action   = "s3:ListBucket"
        Resource = aws_s3_bucket.images.arn
      },
    ]
  })
}
