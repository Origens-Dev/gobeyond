locals {
  static_origin_id  = "gobeyond-static-s3"
  dynamic_origin_id = "gobeyond-private-alb"
  static_paths      = distinct(concat(var.static_route_paths, var.public_asset_paths))
  static_prefixes   = distinct(concat(var.static_route_prefixes, ["/_gobeyond/assets/", "/_gobeyond/static/", "/_gobeyond/manifest/"]))
}

resource "aws_s3_bucket" "static" {
  bucket_prefix = "${var.name}-static-"
}

resource "aws_s3_bucket_public_access_block" "static" {
  bucket                  = aws_s3_bucket.static.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "static" {
  bucket = aws_s3_bucket.static.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "static" {
  bucket = aws_s3_bucket.static.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "static" {
  bucket = aws_s3_bucket.static.id

  rule {
    id     = "expire-noncurrent-artifacts"
    status = "Enabled"

    noncurrent_version_expiration {
      noncurrent_days = var.static_asset_retention_days
    }
  }
}

resource "aws_cloudfront_origin_access_control" "static" {
  name                              = "${var.name}-static"
  description                       = "CloudFront OAC for immutable GoBeyond browser assets and static documents"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

resource "aws_cloudfront_vpc_origin" "app" {
  vpc_origin_endpoint_config {
    arn                    = aws_lb.app.arn
    name                   = "${var.name}-private-alb"
    http_port              = 80
    https_port             = 443
    origin_protocol_policy = "http-only"

    origin_ssl_protocols {
      items    = ["TLSv1.2"]
      quantity = 1
    }
  }
}

resource "aws_cloudfront_function" "route_selector" {
  name    = "${var.name}-route-selector"
  runtime = "cloudfront-js-2.0"
  comment = "Selects S3 for generated static documents/assets and the private ALB for Go-rendered routes."
  publish = true
  code = templatefile("${path.module}/route-selector.js.tftpl", {
    static_origin_id  = local.static_origin_id
    dynamic_origin_id = local.dynamic_origin_id
    static_paths      = jsonencode(local.static_paths)
    static_prefixes   = jsonencode(local.static_prefixes)
  })
}

data "aws_cloudfront_cache_policy" "origin_cache_control" {
  name = "Managed-UseOriginCacheControlHeaders"
}

data "aws_cloudfront_origin_request_policy" "all_viewer" {
  # Forward the viewer Host so the Go runtime can validate it against the
  # configured canonical public origin. ALB health probes use the dedicated
  # host-independent health endpoints.
  name = "Managed-AllViewer"
}

resource "aws_cloudfront_distribution" "this" {
  enabled             = true
  is_ipv6_enabled     = true
  comment             = "${var.name} GoBeyond deployment"
  default_root_object = ""
  aliases             = var.aliases
  http_version        = "http2and3"
  price_class         = "PriceClass_100"

  origin {
    domain_name              = aws_s3_bucket.static.bucket_regional_domain_name
    origin_id                = local.static_origin_id
    origin_access_control_id = aws_cloudfront_origin_access_control.static.id
  }

  origin {
    domain_name = aws_lb.app.dns_name
    origin_id   = local.dynamic_origin_id

    vpc_origin_config {
      vpc_origin_id = aws_cloudfront_vpc_origin.app.id
    }
  }

  default_cache_behavior {
    target_origin_id         = local.dynamic_origin_id
    viewer_protocol_policy   = "redirect-to-https"
    allowed_methods          = ["GET", "HEAD", "OPTIONS", "PUT", "PATCH", "POST", "DELETE"]
    cached_methods           = ["GET", "HEAD", "OPTIONS"]
    compress                 = true
    cache_policy_id          = data.aws_cloudfront_cache_policy.origin_cache_control.id
    origin_request_policy_id = data.aws_cloudfront_origin_request_policy.all_viewer.id

    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.route_selector.arn
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = length(var.aliases) == 0
    acm_certificate_arn            = length(var.aliases) == 0 ? null : var.acm_certificate_arn
    ssl_support_method             = length(var.aliases) == 0 ? null : "sni-only"
    minimum_protocol_version       = length(var.aliases) == 0 ? "TLSv1" : "TLSv1.2_2021"
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  lifecycle {
    precondition {
      condition     = length(var.aliases) == 0 || var.acm_certificate_arn != null
      error_message = "acm_certificate_arn is required when aliases are configured. The certificate must be in us-east-1."
    }
  }
}

data "aws_iam_policy_document" "static_bucket" {
  statement {
    sid       = "CloudFrontReadOnly"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.static.arn}/*"]

    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.this.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "static" {
  bucket = aws_s3_bucket.static.id
  policy = data.aws_iam_policy_document.static_bucket.json
}
