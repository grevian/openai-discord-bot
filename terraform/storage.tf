locals {
  images_bucket_name       = "openai-discord-bot-test-openaidiscordbotimagesbu-20x5frjd8d9"
  conversations_table_name = "openai-discord-bot-test-openai-discord-bot-aiDiscordBotConversations"
}

resource "aws_s3_bucket" "images" {
  bucket = local.images_bucket_name

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "images" {
  bucket = aws_s3_bucket.images.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "images" {
  bucket = aws_s3_bucket.images.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = false
  restrict_public_buckets = false
}

resource "aws_s3_bucket_ownership_controls" "images" {
  bucket = aws_s3_bucket.images.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

data "aws_iam_policy_document" "images_policy" {
  statement {
    sid    = "AllowCloudFrontServicePrincipal"
    effect = "Allow"

    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }

    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.images.arn}/*"]

    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.images.arn]
    }
  }

  statement {
    sid    = "ForceHTTPS"
    effect = "Deny"

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    actions = ["s3:*"]

    resources = [
      "${aws_s3_bucket.images.arn}/*",
      aws_s3_bucket.images.arn,
    ]

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "images" {
  bucket = aws_s3_bucket.images.id
  policy = data.aws_iam_policy_document.images_policy.json
}

moved {
  from = aws_s3_bucket_policy.images[0]
  to   = aws_s3_bucket_policy.images
}

resource "aws_dynamodb_table" "conversations" {
  name                        = local.conversations_table_name
  billing_mode                = "PAY_PER_REQUEST"
  hash_key                    = "thread_id"
  range_key                   = "message_unix_time"
  deletion_protection_enabled = true

  attribute {
    name = "thread_id"
    type = "S"
  }

  attribute {
    name = "message_unix_time"
    type = "N"
  }

  lifecycle {
    prevent_destroy = true
  }
}
