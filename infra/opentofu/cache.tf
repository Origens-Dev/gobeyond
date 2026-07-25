resource "aws_elasticache_serverless_cache" "app" {
  count = var.create_cache ? 1 : 0

  engine             = "valkey"
  name               = substr("${var.name}-cache", 0, 40)
  description        = "Private shared cache for the ${var.name} GoBeyond deployment."
  kms_key_id         = var.cache_kms_key_arn
  security_group_ids = [aws_security_group.cache[0].id]
  subnet_ids         = values(aws_subnet.private)[*].id
}
