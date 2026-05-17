# Resources provisioned only after `copilot env delete` removes the old
# bucket policy and Route53 alias. Pass -var="post_cutover=true" on the
# follow-up apply.

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
  count = var.post_cutover ? 1 : 0

  bucket = aws_s3_bucket.images.id
  policy = data.aws_iam_policy_document.images_policy.json
}

resource "aws_route53_record" "domain" {
  count = var.post_cutover ? 1 : 0

  zone_id = data.aws_route53_zone.main.zone_id
  name    = local.domain
  type    = "A"

  alias {
    name                   = aws_cloudfront_distribution.images.domain_name
    zone_id                = aws_cloudfront_distribution.images.hosted_zone_id
    evaluate_target_health = false
  }
}
