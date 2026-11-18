# Private bucket for binaries built in GitHub Actions and installed on the
# instance at boot (user-data's fetch_artifact). Unlike the images bucket,
# nothing serves it publicly: no CloudFront, all public access blocked.
#
# Key layout: artifacts/<name>/<version>/<file>, with <file>.sha256 next to
# each file (sha256sum format). A version is never overwritten: a rebuild
# gets a new <version>. Uploads come from GitHub Actions with the apply role
# (AWS_ROLE_TO_ASSUME); the instance role can only read artifacts/*.
resource "aws_s3_bucket" "artifacts" {
  bucket = "${local.name_prefix}-artifacts"
}

resource "aws_s3_bucket_public_access_block" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Versioning keeps an accidentally overwritten or deleted artifact
# recoverable for the noncurrent-version window below.
resource "aws_s3_bucket_versioning" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  versioning_configuration {
    status = "Enabled"
  }
}

# Overwritten or deleted object versions expire after 30 days. Current
# objects never expire by age here: the collector is pinned to one version
# for months, and the instance must be able to fetch it on any refresh. A
# per-name age rule (e.g. old bot builds under artifacts/danbot/) can be added
# as its own rule with a prefix filter.
resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  rule {
    id     = "expire-noncurrent"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = 30
    }

    expiration {
      expired_object_delete_marker = true
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }

  depends_on = [aws_s3_bucket_versioning.artifacts]
}

data "aws_iam_policy_document" "artifacts_bucket" {
  statement {
    sid    = "ForceHTTPS"
    effect = "Deny"

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    actions = ["s3:*"]

    resources = [
      aws_s3_bucket.artifacts.arn,
      "${aws_s3_bucket.artifacts.arn}/*",
    ]

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  policy = data.aws_iam_policy_document.artifacts_bucket.json

  depends_on = [aws_s3_bucket_public_access_block.artifacts]
}

# Read-only, and only under artifacts/: no list, no write.
resource "aws_iam_role_policy" "bot_artifacts" {
  name = "artifacts-read"
  role = aws_iam_role.bot.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid      = "ReadArtifacts"
      Effect   = "Allow"
      Action   = "s3:GetObject"
      Resource = "${aws_s3_bucket.artifacts.arn}/artifacts/*"
    }]
  })
}
