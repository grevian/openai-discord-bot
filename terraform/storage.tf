locals {
  images_bucket_name       = "openai-discord-bot-test-openaidiscordbotimagesbu-20x5frjd8d9"
  conversations_table_name = "openai-discord-bot-test-openai-discord-bot-aiDiscordBotConversations"
}

import {
  to = aws_s3_bucket.images
  id = local.images_bucket_name
}

resource "aws_s3_bucket" "images" {
  bucket = local.images_bucket_name

  lifecycle {
    prevent_destroy = true
  }
}

import {
  to = aws_s3_bucket_server_side_encryption_configuration.images
  id = local.images_bucket_name
}

resource "aws_s3_bucket_server_side_encryption_configuration" "images" {
  bucket = aws_s3_bucket.images.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

import {
  to = aws_s3_bucket_public_access_block.images
  id = local.images_bucket_name
}

resource "aws_s3_bucket_public_access_block" "images" {
  bucket = aws_s3_bucket.images.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = false
  restrict_public_buckets = false
}

import {
  to = aws_s3_bucket_ownership_controls.images
  id = local.images_bucket_name
}

resource "aws_s3_bucket_ownership_controls" "images" {
  bucket = aws_s3_bucket.images.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

data "aws_iam_policy_document" "images_force_https" {
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

import {
  to = aws_s3_bucket_policy.images
  id = local.images_bucket_name
}

resource "aws_s3_bucket_policy" "images" {
  bucket = aws_s3_bucket.images.id
  policy = data.aws_iam_policy_document.images_force_https.json
}

import {
  to = aws_dynamodb_table.conversations
  id = local.conversations_table_name
}

resource "aws_dynamodb_table" "conversations" {
  name         = local.conversations_table_name
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "thread_id"
  range_key    = "message_unix_time"

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
